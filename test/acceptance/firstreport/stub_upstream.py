"""Serve one synthetic streaming completion through the production relay."""

import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
from threading import Thread
import time

import model_relay


REPORT = "# Switchback first report fixture\n\nThe isolated report and acceptance path completed."
UPSTREAM_KEY = b"switchback-stub-upstream"
CONTROL = Path("/run/fixture-control")


class Upstream(BaseHTTPRequestHandler):
    def do_POST(self):
        print("fixture-upstream-request", flush=True)
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if (self.path != model_relay.COMPLETION_PATH
                or self.headers.get("Authorization") != "Bearer " + UPSTREAM_KEY.decode()
                or body.get("model") != model_relay.MODEL
                or body.get("stream") is not True):
            self.send_error(400)
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Connection", "close")
        self.end_headers()
        completion = {
            "id": "chatcmpl-switchback-fixture", "object": "chat.completion.chunk",
            "created": int(time.time()), "model": model_relay.MODEL,
        }

        def chunk(delta, end=None):
            frame = {**completion, "choices": [{"index": 0, "delta": delta, "finish_reason": end}]}
            self.wfile.write(("data: " + json.dumps(frame) + "\n\n").encode())
            self.wfile.flush()

        chunk({"role": "assistant", "content": ""})
        # Keep the first Turn active while the runner probes LiteLLM a second time.
        deadline = time.monotonic() + 25
        while not (CONTROL / "release").exists() and time.monotonic() < deadline:
            time.sleep(0.05)
        chunk({"content": REPORT})
        chunk({}, "stop")
        usage = {"prompt_tokens": 24, "completion_tokens": 32, "total_tokens": 56}
        self.wfile.write(("data: " + json.dumps({**completion, "choices": [], "usage": usage})
                          + "\n\ndata: [DONE]\n\n").encode())

    def log_message(self, *_):
        pass


def main():
    token = model_relay.read_credential(Path("/run/fixture/relay-credential"))
    with ThreadingHTTPServer(("127.0.0.1", 0), Upstream) as upstream:
        Thread(target=upstream.serve_forever, daemon=True).start()

        def connect():
            return http.client.HTTPConnection("127.0.0.1", upstream.server_port, timeout=35)

        class Relay(model_relay.make_handler(Path("/run/fixture-permit"), token, UPSTREAM_KEY, connect)):
            def log_request(self, code="-", size="-"):
                if self.command == "POST":
                    print(f"fixture-relay-status={code}", flush=True)

        try:
            with ThreadingHTTPServer(("0.0.0.0", 8080), Relay) as server:
                print("stub-relay-ready", flush=True)
                server.serve_forever()
        finally:
            upstream.shutdown()


if __name__ == "__main__":
    main()
