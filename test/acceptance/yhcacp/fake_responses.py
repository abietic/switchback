"""Serve exactly one scripted Responses stream for each reviewed delivery."""

import hmac
from http.client import HTTPConnection
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path

import secrets
from threading import Thread

import model_relay


FIRST = "OFFLINE-LETS-QUANT-INITIAL-REPORT"
REWORK = "REWORK-RESEARCH-REPORT"
ROOT = Path("/run/fixture")
PERMITS = Path("/run/fixture-permit")
CONTROL = Path("/run/fixture-control")


def text(content):
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "".join(item.get("text", "") for item in content if isinstance(item, dict))
    return ""


class Handler(BaseHTTPRequestHandler):
    def log_message(self, _format, *args):
        pass

    def refuse(self, status, reason):
        print(f"fixture-refusal {status} {reason}", flush=True)
        body = json.dumps({"error": {"message": reason, "type": "invalid_request_error"}}).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        if self.path != "/responses":
            self.refuse(404, "responses_route_required")
            return
        auth = self.headers.get_all("Authorization", [])
        if len(auth) != 1 or not hmac.compare_digest(auth[0], self.server.authorization):
            self.refuse(401, "fixture_credential_required")
            return
        try:
            size = int(self.headers.get("Content-Length", "0"))
            if not 0 < size <= model_relay.MAX_BODY_BYTES:
                raise ValueError("request size")
            request = json.loads(self.rfile.read(size))
            messages = request["input"]
            if request["model"] != model_relay.MODEL or request["stream"] is not True or not isinstance(messages, list):
                raise ValueError("request shape")
            latest = next(text(message.get("content")) for message in reversed(messages)
                          if isinstance(message, dict) and message.get("role") == "user")
            if REWORK in latest:
                version = 2
                summaries = json.loads((CONTROL / "review-findings.json").read_bytes())
                if len(summaries) != 3 or not all(summary in latest for summary in summaries):
                    self.refuse(400, "review_findings_missing_from_rework")
                    return
                history = [text(message.get("content")) for message in messages
                           if isinstance(message, dict) and message.get("role") == "assistant"]
                if self.server.reports[1] not in history:
                    self.refuse(400, "first_report_missing_from_resident_context")
                    return
            elif FIRST in latest:
                version = 1
            else:
                self.refuse(400, "unexpected_prompt")
                return
        except (KeyError, ValueError, StopIteration, OSError, TypeError):
            self.refuse(400, "invalid_fixture_request")
            return

        if version == 2 and not (PERMITS / "first" / "permit.used").is_file():
            self.refuse(409, "first_report_has_no_permit")
            return
        if not model_relay.consume_permit(PERMITS / f"v{version}"):
            self.refuse(409, "delivery_permit_already_consumed")
            return
        print(f"fixture-upstream-request v{version} /responses", flush=True)
        self.respond(version)

    def respond(self, version):
        report = self.server.reports[version]
        part = {"type": "output_text", "text": report, "annotations": [], "logprobs": []}
        message_id = f"msg_switchback_report_{version}"
        item = {"type": "message", "id": message_id, "role": "assistant", "status": "completed", "content": [part]}
        response = {"id": f"resp_switchback_report_{version}", "object": "response", "created_at": 1795510000,
                    "status": "completed", "model": model_relay.MODEL, "output": [item],
                    "parallel_tool_calls": False, "tools": [],
                    "usage": {"input_tokens": 48, "output_tokens": 96, "total_tokens": 144,
                              "input_tokens_details": {"cached_tokens": 0}, "output_tokens_details": {"reasoning_tokens": 0}}}
        events = [
            {"type": "response.created", "response": dict(response, status="in_progress", output=[], usage=None)},
            {"type": "response.output_item.added", "output_index": 0, "item": dict(item, status="in_progress", content=[])},
            {"type": "response.content_part.added", "output_index": 0, "content_index": 0, "item_id": message_id, "part": dict(part, text="")},
            {"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "item_id": message_id, "delta": report, "logprobs": []},
            {"type": "response.output_text.done", "output_index": 0, "content_index": 0, "item_id": message_id, "text": report, "logprobs": []},
            {"type": "response.content_part.done", "output_index": 0, "content_index": 0, "item_id": message_id, "part": part},
            {"type": "response.output_item.done", "output_index": 0, "item": item},
            {"type": "response.completed", "response": response},
        ]
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Connection", "close")
        self.end_headers()
        for sequence, event in enumerate(events):
            event["sequence_number"] = sequence
            payload = json.dumps(event, ensure_ascii=False)
            self.wfile.write(f"event: {event['type']}\ndata: {payload}\n\n".encode())
            self.wfile.flush()


if __name__ == "__main__":
    for version in (1, 2):
        (PERMITS / f"v{version}").mkdir(mode=0o700, exist_ok=False)
    (PERMITS / "current").mkdir(mode=0o700, exist_ok=False)
    upstream_key = secrets.token_hex(32).encode()
    relay_token = model_relay.read_credential(ROOT / "relay-credential")
    with ThreadingHTTPServer(("127.0.0.1", 0), Handler) as upstream:
        upstream.authorization = "Bearer " + upstream_key.decode()
        upstream.reports = {version: (ROOT / f"report-v{version}.md").read_text(encoding="utf-8").strip()
                            for version in (1, 2)}
        handler = model_relay.make_handler(
            PERMITS / "current", relay_token, upstream_key,
            connect=lambda: HTTPConnection("127.0.0.1", upstream.server_port, timeout=15),
            api_family="responses",
        )
        thread = Thread(target=upstream.serve_forever, daemon=True)
        thread.start()
        try:
            with ThreadingHTTPServer(("0.0.0.0", 8080), handler) as relay:
                print("stub-relay-ready", flush=True)
                relay.serve_forever()
        finally:
            upstream.shutdown()
            thread.join()
