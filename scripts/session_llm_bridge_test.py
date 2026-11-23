"""Protocol checks; no external inference or application credentials required."""
import importlib.util
import json
from pathlib import Path
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request

spec = importlib.util.spec_from_file_location("bridge", Path(__file__).with_name("session_llm_bridge.py"))
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)


class ProtocolTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.server = bridge.Bridge(("127.0.0.1", 0), self.tmp.name, 0.5)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.url = f"http://127.0.0.1:{self.server.server_port}/v1/chat/completions"

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()
        self.tmp.cleanup()

    def exchange(self, request, message):
        failures = []

        def respond():
            try:
                until = time.monotonic() + 2
                while time.monotonic() < until:
                    paths = list(Path(self.tmp.name).glob("*/request.json"))
                    if paths:
                        bridge.atomic_json(paths[0].with_name("response.json"), message)
                        return
                    time.sleep(0.01)
                raise AssertionError("request did not reach mailbox")
            except Exception as exc:
                failures.append(exc)

        worker = threading.Thread(target=respond)
        worker.start()
        try:
            req = urllib.request.Request(self.url, data=json.dumps(request).encode(), headers={"Content-Type": "application/json"})
            with urllib.request.urlopen(req, timeout=3) as response:
                return response.read().decode()
        finally:
            worker.join()
            if failures:
                raise failures[0]

    def test_text(self):
        raw = self.exchange({"model": bridge.MODEL, "messages": [{"role": "user", "content": "ping"}]}, {"role": "assistant", "content": "pong"})
        response = json.loads(raw)
        self.assertEqual(response["choices"][0]["message"]["content"], "pong")
        self.assertNotIn("usage", response)

    def test_streamed_tool_call(self):
        request = {"model": bridge.MODEL, "messages": [], "stream": True, "tools": [{"type": "function", "function": {"name": "echo"}}]}
        message = {"role": "assistant", "content": None, "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "echo", "arguments": '{"text":"hello"}'}}]}
        raw = self.exchange(request, message)
        frames = [line[6:] for line in raw.splitlines() if line.startswith("data: ")]
        self.assertEqual(frames[-1], "[DONE]")
        self.assertEqual(json.loads(frames[0])["choices"][0]["delta"]["tool_calls"][0]["index"], 0)
        self.assertEqual(json.loads(frames[1])["choices"][0]["finish_reason"], "tool_calls")

    def test_unknown_tool_rejected(self):
        message = {"role": "assistant", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "absent", "arguments": "{}"}}]}
        with self.assertRaises(urllib.error.HTTPError) as caught:
            self.exchange({"model": bridge.MODEL, "messages": []}, message)
        self.assertEqual(caught.exception.code, 502)
        caught.exception.close()

    def test_no_responder_expires(self):
        req = urllib.request.Request(self.url, data=json.dumps({"model": bridge.MODEL, "messages": []}).encode())
        with self.assertRaises(urllib.error.HTTPError) as caught:
            urllib.request.urlopen(req, timeout=3)
        self.assertEqual(caught.exception.code, 504)
        caught.exception.close()


if __name__ == "__main__":
    unittest.main()
