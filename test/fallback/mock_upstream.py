#!/usr/bin/env python3
"""Mock OpenAI-compatible upstream used by the model fallback integration test.

It deliberately fails the primary model with a configurable status and answers
the fallback model with a plain JSON or SSE response, so the proxy's fallback
behavior can be verified without any real provider credentials.
"""

import json
import os
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse

PORT = int(os.environ.get("MOCK_PORT", "18318"))
PRIMARY_MODEL = os.environ.get("MOCK_PRIMARY_MODEL", "fallback-primary")
PRIMARY_STATUS = int(os.environ.get("MOCK_PRIMARY_STATUS", "429"))
BAD_MODEL = os.environ.get("MOCK_BAD_MODEL", "no-fallback-primary")
TARGET_MODEL = os.environ.get("MOCK_TARGET_MODEL", "fallback-target")

COUNTERS = {}
COUNTERS_LOCK = threading.Lock()


def record_request(model):
    with COUNTERS_LOCK:
        COUNTERS[model] = COUNTERS.get(model, 0) + 1


def counter_snapshot():
    with COUNTERS_LOCK:
        return dict(COUNTERS)


def reset_counters():
    with COUNTERS_LOCK:
        COUNTERS.clear()


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        sys.stderr.write("mock: " + fmt % args + "\n")

    def send_json(self, status, payload):
        data = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        path = urlparse(self.path).path
        if path == "/healthz":
            self.send_json(200, {"ok": True})
            return
        if path == "/metrics":
            self.send_json(200, counter_snapshot())
            return
        self.send_json(404, {"error": {"message": "not found"}})

    def do_POST(self):
        path = urlparse(self.path).path
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b"{}"
        if path == "/reset":
            reset_counters()
            self.send_json(200, {"ok": True})
            return
        try:
            body = json.loads(raw or b"{}")
        except json.JSONDecodeError:
            body = {}
        model = body.get("model", "")
        stream = bool(body.get("stream"))
        record_request(model)
        sys.stderr.write(f"mock-request model={model} stream={stream} path={path}\n")
        sys.stderr.flush()

        if path not in ("/v1/chat/completions", "/chat/completions"):
            self.send_json(404, {"error": {"message": "not found"}})
            return
        if model == PRIMARY_MODEL:
            self.send_json(
                PRIMARY_STATUS,
                {"error": {"message": "mock primary failure", "type": "rate_limit_error"}},
            )
            return
        if model == BAD_MODEL:
            self.send_json(
                400,
                {"error": {"message": "mock invalid request", "type": "invalid_request_error"}},
            )
            return
        if model != TARGET_MODEL:
            self.send_json(404, {"error": {"message": "unknown model"}})
            return
        if stream:
            self.send_sse(model)
            return
        self.send_json(
            200,
            {
                "id": "chatcmpl-mock",
                "object": "chat.completion",
                "created": 0,
                "model": model,
                "choices": [
                    {
                        "index": 0,
                        "message": {"role": "assistant", "content": "hello from fallback"},
                        "finish_reason": "stop",
                    }
                ],
                "usage": {"prompt_tokens": 1, "completion_tokens": 3, "total_tokens": 4},
            },
        )

    def send_sse(self, model):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        chunks = [
            {
                "id": "chatcmpl-mock",
                "object": "chat.completion.chunk",
                "created": 0,
                "model": model,
                "choices": [{"index": 0, "delta": {"role": "assistant", "content": "hello "}, "finish_reason": None}],
            },
            {
                "id": "chatcmpl-mock",
                "object": "chat.completion.chunk",
                "created": 0,
                "model": model,
                "choices": [{"index": 0, "delta": {"content": "from fallback"}, "finish_reason": None}],
            },
            {
                "id": "chatcmpl-mock",
                "object": "chat.completion.chunk",
                "created": 0,
                "model": model,
                "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
            },
        ]
        for chunk in chunks:
            self.wfile.write(b"data: " + json.dumps(chunk).encode() + b"\n\n")
            self.wfile.flush()
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()


def main():
    server = ThreadingHTTPServer(("0.0.0.0", PORT), Handler)
    sys.stderr.write(f"mock upstream listening on :{PORT}\n")
    sys.stderr.flush()
    server.serve_forever()


if __name__ == "__main__":
    main()
