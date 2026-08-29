#!/usr/bin/env python3
"""Tiny mock of an OpenAI-compatible /v1/chat/completions endpoint.

Deterministic scripted replies for smoke-testing the harness without a real
model: turn 1 runs `echo hello-from-pty`, turn 2 (once it sees that string
in the terminal output) calls finish_step pass.

Run:  python3 mock_slm_server.py
Then: slmtest run examples/echo-test.md -endpoint http://localhost:8080/v1 -verbose
"""
import json
from http.server import BaseHTTPRequestHandler, HTTPServer

TURN = {"n": 0}


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(length) or b"{}")
        last_user = ""
        for m in body.get("messages", []):
            if m["role"] == "user":
                last_user = m["content"]

        # Only match on ACTUAL terminal output, never on the prompt text
        # (which also mentions "hello-from-pty" in the step's Hint) —
        # otherwise this "smoke test" would pass without ever touching
        # the PTY, which defeats the point of the test.
        if last_user.startswith("Terminal output:") and "hello-from-pty" in last_user:
            action = {
                "action": "finish_step",
                "step_result": "pass",
                "reason": "saw hello-from-pty in terminal output",
            }
        else:
            action = {
                "action": "run_command",
                "command": "echo hello-from-pty",
                "wait_ms": 500,
            }

        content = json.dumps(action)
        resp = {
            "choices": [{"message": {"role": "assistant", "content": content}}]
        }
        payload = json.dumps(resp).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, fmt, *args):
        pass  # keep smoke-test output quiet


if __name__ == "__main__":
    # Bind all interfaces rather than "localhost": on some macOS setups a
    # server bound to the IPv4 loopback specifically never reaches LISTEN,
    # and connections hang until they time out instead of being refused.
    HTTPServer(("", 8080), Handler).serve_forever()
