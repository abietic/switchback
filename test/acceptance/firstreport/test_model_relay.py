from concurrent.futures import ThreadPoolExecutor
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
from tempfile import TemporaryDirectory
from threading import Event, Lock, Thread
import unittest

import model_relay


RELAY_TOKEN = b"fixture-relay-token-only-for-tests"
UPSTREAM_KEY = b"fixture-upstream-key"
EVENT_STREAM = b'data: {"id":"fixture"}\n\ndata: [DONE]\n\n'


class RelayTest(unittest.TestCase):
    def setUp(self):
        temporary = TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.permit = Path(temporary.name)
        self.requests = []
        self.request_lock = Lock()
        self.release = Event()
        self.behavior = "stream"
        fixture = self

        class Upstream(BaseHTTPRequestHandler):
            def do_POST(self):
                payload = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                with fixture.request_lock:
                    fixture.requests.append((self.path, self.headers["Authorization"], payload["model"]))
                if fixture.behavior == "disconnect":
                    return
                if fixture.behavior == "timeout":
                    fixture.release.wait(2)
                    return
                if fixture.behavior == "redirect":
                    self.send_response(307)
                    self.send_header("Location", "/second-request")
                    self.end_headers()
                    return
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Content-Length", str(len(EVENT_STREAM)))
                self.end_headers()
                self.wfile.write(EVENT_STREAM)

            def log_message(self, _format, *args):
                pass

        self.upstream = self.start_server(Upstream)
        self.relay = self.start_relay()
        self.addCleanup(self.release.set)

    def start_server(self, handler):
        server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        Thread(target=server.serve_forever, kwargs={"poll_interval": 0.02}, daemon=True).start()
        return server

    def start_relay(self, api_family="chat-completions"):
        def connect():
            return http.client.HTTPConnection("127.0.0.1", self.upstream.server_port, timeout=0.3)

        return self.start_server(model_relay.make_handler(self.permit, RELAY_TOKEN, UPSTREAM_KEY, connect,
                                                          api_family=api_family))

    def request(self, *, path=model_relay.COMPLETION_PATH, token=RELAY_TOKEN, model=model_relay.MODEL, server=None):
        server = server or self.relay
        connection = http.client.HTTPConnection("127.0.0.1", server.server_port, timeout=5)
        try:
            payload = json.dumps({"model": model, "messages": [{"role": "user", "content": "fixture"}], "stream": True})
            connection.request("POST", path, payload, {"Authorization": b"Bearer " + token})
            with connection.getresponse() as response:
                return response.status, response.read()
        finally:
            connection.close()

    def assert_one_upstream(self):
        self.assertEqual(self.requests, [(model_relay.COMPLETION_PATH, "Bearer " + UPSTREAM_KEY.decode(), model_relay.MODEL)])

    def test_concurrent_requests_stream_only_one_upstream_response(self):
        with ThreadPoolExecutor(max_workers=8) as pool:
            results = list(pool.map(lambda _: self.request(), range(16)))
        self.assertEqual([body for status, body in results if status == 200], [EVENT_STREAM])
        self.assertEqual(sum(status == 409 for status, _ in results), 15)
        self.assert_one_upstream()

    def test_rejected_requests_do_not_spend_the_permit(self):
        self.assertEqual(self.request(path="/other")[0], 404)
        self.assertEqual(self.request(token=b"invalid")[0], 401)
        self.assertEqual(self.request(model="other")[0], 400)
        self.assertFalse(self.requests)
        self.assertEqual(self.request()[0], 200)
        self.assert_one_upstream()

    def test_responses_mode_uses_exact_path_and_shares_the_permit(self):
        with self.assertRaises(ValueError):
            model_relay.make_handler(self.permit, RELAY_TOKEN, UPSTREAM_KEY, api_family="responses")
        responses = self.start_relay("responses")
        self.assertEqual(self.request(path="/responses")[0], 404)
        self.assertEqual(self.request(server=responses)[0], 404)
        self.assertEqual(self.request(path="/responses?extra=1", server=responses)[0], 404)
        self.assertFalse(self.requests)
        self.assertEqual(self.request(path="/responses", server=responses), (200, EVENT_STREAM))
        self.assertEqual(self.request(path="/responses", server=responses)[0], 409)
        self.assertEqual(self.request()[0], 409)
        self.assertEqual(self.requests, [("/responses", "Bearer " + UPSTREAM_KEY.decode(), model_relay.MODEL)])

    def test_redirect_does_not_follow_or_enable_another_relay(self):
        self.behavior = "redirect"
        self.assertEqual(self.request()[0], 502)
        self.assertEqual(self.request(server=self.start_relay())[0], 409)
        self.assert_one_upstream()

    def test_disconnect_does_not_refund(self):
        self.behavior = "disconnect"
        self.assertEqual(self.request()[0], 502)
        self.assertEqual(self.request()[0], 409)
        self.assert_one_upstream()

    def test_timeout_does_not_refund(self):
        self.behavior = "timeout"
        self.assertEqual(self.request()[0], 502)
        self.assertEqual(self.request()[0], 409)
        self.assert_one_upstream()

    def test_unavailable_permit_directory_fails_closed(self):
        self.permit.chmod(0o755)
        self.assertEqual(self.request()[0], 503)
        self.assertFalse(self.requests)


if __name__ == "__main__":
    unittest.main()
