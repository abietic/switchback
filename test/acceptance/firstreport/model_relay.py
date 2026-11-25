"""Single-use DeepSeek relay for the isolated first-report acceptance run."""

import argparse
from contextlib import closing
import hmac
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import stat


UPSTREAM_HOST = "api.deepseek.com"
COMPLETION_PATH = "/chat/completions"
API_PATHS = {"chat-completions": COMPLETION_PATH, "responses": "/responses"}
MODEL = "deepseek-v4-pro"
MAX_BODY_BYTES = 1 << 20


def read_credential(path: Path) -> bytes:
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, "rb") as credential:
        info = os.fstat(credential.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
            raise ValueError("credential must be a private regular file")
        value = credential.read(4097).removesuffix(b"\n")
    if not value or len(value) > 4096 or any(not 33 <= byte <= 126 for byte in value):
        raise ValueError("invalid credential contents")
    return value


def consume_permit(directory: Path) -> bool:
    parent = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        info = os.fstat(parent)
        if info.st_uid != os.geteuid() or info.st_mode & 0o077:
            raise PermissionError("permit directory must be owned and private")
        try:
            marker = os.open("permit.used", os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600, dir_fd=parent)
        except FileExistsError:
            return False
        try:
            os.fsync(marker)
        finally:
            os.close(marker)
        os.fsync(parent)
        return True
    finally:
        os.close(parent)


def open_upstream() -> http.client.HTTPSConnection:
    return http.client.HTTPSConnection(UPSTREAM_HOST, timeout=90)


def make_handler(directory: Path, relay_token: bytes, upstream_key: bytes, connect=open_upstream,
                 *, api_family="chat-completions"):
    if api_family == "responses" and connect is open_upstream:
        raise ValueError("Responses relay requires an explicitly supplied upstream connection")
    request_path = API_PATHS[api_family]
    expected_authorization = b"Bearer " + relay_token
    upstream_authorization = b"Bearer " + upstream_key

    class Handler(BaseHTTPRequestHandler):
        def setup(self):
            super().setup()
            self.connection.settimeout(15)

        def log_message(self, _format, *args):
            pass

        def do_POST(self):
            if self.path != request_path:
                self.send_error(404)
                return
            supplied = self.headers.get_all("Authorization", [])
            if len(supplied) != 1 or not hmac.compare_digest(supplied[0].encode("latin-1"), expected_authorization):
                self.send_error(401)
                return

            lengths = self.headers.get_all("Content-Length", [])
            if self.headers.get_all("Transfer-Encoding") or len(lengths) != 1:
                self.send_error(400)
                return
            length = lengths[0]
            if len(length) > 7 or not length.isascii() or not length.isdecimal():
                self.send_error(400)
                return
            size = int(length)
            if not 0 < size <= MAX_BODY_BYTES:
                self.send_error(413)
                return
            try:
                body = self.rfile.read(size)
                if len(body) != size:
                    self.send_error(400)
                    return
                payload = json.loads(body)
                if not isinstance(payload, dict) or payload.get("model") != MODEL:
                    self.send_error(400)
                    return
                body = json.dumps(payload, allow_nan=False).encode()
            except (ValueError, OSError):
                self.send_error(400)
                return

            try:
                if not consume_permit(directory):
                    self.send_error(409, "Request permit already consumed")
                    return
            except OSError:
                self.send_error(503, "Request permit unavailable")
                return

            self._forward(body)

        def _forward(self, body):
            downstream_started = False
            try:
                # HTTPConnection sends once and never follows redirects.
                with closing(connect()) as upstream:
                    upstream.request("POST", request_path, body, {
                        "Authorization": upstream_authorization,
                        "Content-Type": "application/json",
                        "Connection": "close",
                    })
                    with upstream.getresponse() as response:
                        content_type = response.headers.get_content_type()
                        if response.status != 200 or content_type not in ("application/json", "text/event-stream"):
                            self.send_error(502, "Upstream did not return a completion")
                            return
                        self.send_response(200)
                        self.send_header("Content-Type", content_type)
                        self.send_header("Connection", "close")
                        self.end_headers()
                        downstream_started = True
                        while chunk := response.read1(8192):
                            self.wfile.write(chunk)
                            self.wfile.flush()
            except (OSError, http.client.HTTPException):
                if not downstream_started:
                    self.send_error(502, "Upstream request failed")

    return Handler


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8080)
    parser.add_argument("--permit-directory", type=Path, required=True)
    parser.add_argument("--relay-token-file", type=Path, required=True)
    parser.add_argument("--upstream-key-file", type=Path, required=True)
    args = parser.parse_args()
    relay_token = read_credential(args.relay_token_file)
    upstream_key = read_credential(args.upstream_key_file)
    if len(relay_token) < 32 or hmac.compare_digest(relay_token, upstream_key):
        parser.error("relay token must be independent of the upstream credential and at least 32 bytes")
    handler = make_handler(args.permit_directory, relay_token, upstream_key)
    with ThreadingHTTPServer((args.host, args.port), handler) as server:
        server.serve_forever()


if __name__ == "__main__":
    main()
