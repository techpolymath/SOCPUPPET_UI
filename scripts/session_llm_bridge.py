#!/usr/bin/env python3
"""Experimental loopback Chat Completions mailbox serviced by an active chat agent.

No model runs inside this server. Each request waits for a response.json written
by the chat agent. Never point unattended production tasks at this endpoint.
"""
import argparse
import json
import os
from pathlib import Path
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MODEL = "session-bridge"
MAX_BODY = 16 * 1024 * 1024


def atomic_json(path, value):
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps(value, ensure_ascii=False, indent=2))
    tmp.replace(path)


def validate_message(message, request):
    if not isinstance(message, dict) or message.get("role") != "assistant":
        raise ValueError("response must be an assistant message")
    if message.get("content") is not None and not isinstance(message["content"], str):
        raise ValueError("content must be text or null")
    calls = message.get("tool_calls", [])
    if not isinstance(calls, list):
        raise ValueError("tool_calls must be a list")
    tools = {t.get("function", {}).get("name") for t in request.get("tools", [])}
    for call in calls:
        fn = call.get("function", {})
        if call.get("type") != "function" or not call.get("id") or fn.get("name") not in tools:
            raise ValueError("tool call must identify a function offered in the request")
        if not isinstance(fn.get("arguments"), str):
            raise ValueError("function arguments must be a JSON string")
        json.loads(fn["arguments"])
    if not calls and not isinstance(message.get("content"), str):
        raise ValueError("response needs content or tool calls")
    return message


class Bridge(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, address, directory, timeout, max_pending=16):
        self.directory = Path(directory).resolve()
        self.directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.directory.chmod(0o700)
        self.timeout_seconds = timeout
        self.slots = threading.BoundedSemaphore(max_pending)
        super().__init__(address, Handler)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass  # Request contents and credentials never go to logs.

    def reply(self, status, value):
        body = json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def error(self, status, message):
        self.reply(status, {"error": {"message": message, "type": "session_bridge_error"}})

    def do_GET(self):
        if self.path == "/health":
            self.reply(200, {"ok": True, "mode": "active-session-mailbox", "automatic_inference": False})
        elif self.path == "/v1/models":
            self.reply(200, {"object": "list", "data": [{"id": MODEL, "object": "model", "created": 0, "owned_by": "local-session"}]})
        else:
            self.error(404, "Not found")

    def do_POST(self):
        if self.path != "/v1/chat/completions":
            self.error(404, "Only /v1/chat/completions is supported")
            return
        if not self.server.slots.acquire(blocking=False):
            self.error(429, "Mailbox full; reduce concurrent agents or increase --max-pending")
            return
        try:
            self.handle_completion()
        except (BrokenPipeError, ConnectionResetError):
            pass
        finally:
            self.server.slots.release()

    def handle_completion(self):
        self.connection.settimeout(10)
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if not 0 < length <= MAX_BODY:
                raise ValueError("Request size must be between 1 byte and 16 MiB")
            request = json.loads(self.rfile.read(length))
            if not isinstance(request, dict) or not isinstance(request.get("messages"), list):
                raise ValueError("messages must be a list")
            if request.get("model") != MODEL:
                raise ValueError("Use model session-bridge")
            if request.get("n", 1) != 1:
                raise ValueError("Only n=1 is supported")
        except (ValueError, OSError) as exc:
            self.error(400, str(exc))
            return
        ident = uuid.uuid4().hex
        folder = self.server.directory / ident
        folder.mkdir(mode=0o700)
        atomic_json(folder / "request.json", request)
        atomic_json(folder / "status.json", {"state": "pending", "created": time.time()})
        deadline = time.monotonic() + self.server.timeout_seconds
        while time.monotonic() < deadline:
            if (folder / "response.json").exists():
                try:
                    message = validate_message(json.loads((folder / "response.json").read_text()), request)
                except (ValueError, TypeError, AttributeError) as exc:
                    atomic_json(folder / "status.json", {"state": "invalid_response", "error": str(exc)})
                    self.error(502, str(exc))
                    return
                finish = "tool_calls" if message.get("tool_calls") else "stop"
                base = {"id": "chatcmpl-" + ident, "created": int(time.time()), "model": MODEL}
                try:
                    if request.get("stream"):
                        self.send_response(200)
                        self.send_header("Content-Type", "text/event-stream")
                        self.send_header("Cache-Control", "no-cache")
                        self.send_header("Connection", "close")
                        self.end_headers()
                        delta = dict(message)
                        if delta.get("tool_calls"):
                            delta["tool_calls"] = [dict(c, index=i) for i, c in enumerate(delta["tool_calls"])]
                        for data, reason in [(delta, None), ({}, finish)]:
                            chunk = dict(base, object="chat.completion.chunk", choices=[{"index": 0, "delta": data, "finish_reason": reason}])
                            self.wfile.write(("data: " + json.dumps(chunk) + "\n\n").encode())
                        self.wfile.write(b"data: [DONE]\n\n")
                        self.wfile.flush()
                        self.close_connection = True
                    else:
                        self.reply(200, dict(base, object="chat.completion", choices=[{"index": 0, "message": message, "finish_reason": finish}]))
                    # Usage is deliberately omitted: this mailbox cannot measure tokens.
                    atomic_json(folder / "status.json", {"state": "delivered", "finished": time.time()})
                except (BrokenPipeError, ConnectionResetError):
                    atomic_json(folder / "status.json", {"state": "disconnected", "finished": time.time()})
                return
            time.sleep(0.1)
        atomic_json(folder / "status.json", {"state": "expired", "finished": time.time()})
        self.error(504, "No active chat agent supplied a response before the mailbox deadline")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, default=8790)
    parser.add_argument("--directory", default="data/session-llm-bridge")
    parser.add_argument("--timeout", type=float, default=120)
    parser.add_argument("--max-pending", type=int, default=16)
    actions = parser.add_mutually_exclusive_group()
    actions.add_argument("--pending", action="store_true", help="List pending request IDs")
    actions.add_argument("--show", metavar="ID", help="Print one request, including its tool schemas")
    actions.add_argument("--reply", metavar="ID", help="Answer one pending request")
    parser.add_argument("--text", help="Text for --reply")
    parser.add_argument("--message-file", help="Assistant message JSON for --reply; - reads stdin")
    args = parser.parse_args()
    os.umask(0o077)
    directory = Path(args.directory).resolve()
    if args.pending:
        pending = []
        for path in sorted(directory.glob("*/status.json")):
            status = json.loads(path.read_text())
            if status.get("state") == "pending":
                pending.append(dict(status, id=path.parent.name))
        print(json.dumps(pending, indent=2))
        return
    ident = args.show or args.reply
    if ident:
        if len(ident) != 32 or any(c not in "0123456789abcdef" for c in ident):
            parser.error("Invalid request ID")
        folder = directory / ident
        request = json.loads((folder / "request.json").read_text())
        if args.show:
            print(json.dumps(request, ensure_ascii=False, indent=2))
            return
        if json.loads((folder / "status.json").read_text()).get("state") != "pending":
            parser.error("Request is no longer pending")
        if (args.text is None) == (args.message_file is None):
            parser.error("Use exactly one of --text or --message-file")
        if args.message_file:
            import sys
            raw = sys.stdin.read() if args.message_file == "-" else Path(args.message_file).read_text()
            message = json.loads(raw)
        else:
            message = {"role": "assistant", "content": args.text}
        validate_message(message, request)
        atomic_json(folder / "response.json", message)
        print("Response queued for " + ident)
        return
    if args.max_pending < 1:
        parser.error("--max-pending must be positive")
    server = Bridge(("127.0.0.1", args.port), args.directory, args.timeout, args.max_pending)
    print(f"Mailbox listening at http://127.0.0.1:{args.port}/v1; no automatic inference", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
