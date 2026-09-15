#!/usr/bin/env python3
"""Logging reverse proxy in front of an OpenAI-compatible endpoint.

Records every chat-completions request/response pair to a JSONL file, so a
run's traces can be turned into fine-tuning data later. Capturing at the wire
rather than reconstructing from `-json` reports is deliberate: the report
carries each turn's user prompt but not the composed system prompt or the
exact message array, and that composition (driver fragment + action list +
judgement rules) is precisely what a narrow fine-tune needs to learn against.

  python3 scripts/trace_proxy.py --listen 8099 --upstream http://localhost:8084 \
      --log traces/qwen.jsonl
"""
import argparse, json, threading, urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOCK = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    upstream = ""
    logpath = ""

    def log_message(self, *a):  # keep the console quiet; the JSONL is the record
        pass

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        req = urllib.request.Request(self.upstream + self.path, body,
                                     {"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=600) as r:
                resp, code = r.read(), r.status
        except urllib.error.HTTPError as e:
            resp, code = e.read(), e.code

        if self.path.endswith("/chat/completions"):
            try:
                rq, rs = json.loads(body), json.loads(resp)
                msg = rs["choices"][0]["message"]
                with LOCK, open(self.logpath, "a") as f:
                    f.write(json.dumps({
                        "messages": rq.get("messages"),
                        "tools": rq.get("tools"),
                        "reply": msg.get("content") or "",
                        "tool_calls": msg.get("tool_calls"),
                    }) + "\n")
            except (ValueError, KeyError, IndexError):
                pass  # a malformed exchange is not worth killing the run over

        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(resp)))
        self.end_headers()
        self.wfile.write(resp)

    def do_GET(self):
        try:
            with urllib.request.urlopen(self.upstream + self.path, timeout=60) as r:
                resp, code = r.read(), r.status
        except urllib.error.HTTPError as e:
            resp, code = e.read(), e.code
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(resp)))
        self.end_headers()
        self.wfile.write(resp)


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--listen", type=int, default=8099)
    ap.add_argument("--upstream", default="http://localhost:8084")
    ap.add_argument("--log", default="traces/qwen.jsonl")
    a = ap.parse_args()
    Handler.upstream, Handler.logpath = a.upstream.rstrip("/"), a.log
    import pathlib; pathlib.Path(a.log).parent.mkdir(parents=True, exist_ok=True)
    print(f"proxy :{a.listen} -> {a.upstream}, logging to {a.log}", flush=True)
    ThreadingHTTPServer(("127.0.0.1", a.listen), Handler).serve_forever()
