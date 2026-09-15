#!/usr/bin/env python3
"""Route ACTION turns to a fast small model and VERDICT turns to a trusted one.

The trap suite shows the two halves of a turn loop have very different
reliability requirements. Choosing the next action is cheap to get wrong — a
bad action produces visibly wrong terminal state and the model gets another
turn. Choosing a verdict is expensive to get wrong: the harness delegates
pass/fail to the model by design, so a fabricated pass is invisible.

So split them. This proxy asks the fast model first; if the reply is anything
other than finish_step, it is returned as-is. If the fast model wants to end
the step — or produced something unparseable — the SAME request is re-sent to
the trusted model and its reply is returned instead. The trusted model is the
only thing that can ever write a verdict.

Neither slmtest nor the runner needs to know: this is one OpenAI-compatible
endpoint that happens to have two models behind it.

  python3 scripts/split_proxy.py --listen 8100 \
      --fast http://localhost:8087/v1 --fast-model traces/lfm-tuned \
      --judge http://localhost:8084/v1 --judge-model mlx-community/Qwen3.5-9B-8bit
"""
import argparse, json, re, threading, urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

STATS = {"fast": 0, "judge": 0, "judge_unparseable": 0}
LOCK = threading.Lock()


def strip_fence(text):
    t = (text or "").strip()
    if t.startswith("```"):
        t = re.sub(r"^```[a-zA-Z]*\n?", "", t)
        t = re.sub(r"\n?```$", "", t)
    return t.strip()


def wants_verdict(content):
    """(is_finish_step, parsed_ok) for a candidate reply."""
    try:
        obj = json.loads(strip_fence(content))
    except (ValueError, TypeError):
        return False, False
    return obj.get("action") in ("finish_step", "abort_test"), True


def base(u):
    """Upstreams are given as OpenAI base URLs (.../v1) but the incoming path
    already carries /v1, so the suffix has to come off to avoid /v1/v1/..."""
    u = u.rstrip("/")
    return u[:-3].rstrip("/") if u.endswith("/v1") else u


def post(url, body):
    req = urllib.request.Request(url, body, {"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=600) as r:
            return r.read(), r.status
    except urllib.error.HTTPError as e:
        return e.read(), e.code


class Handler(BaseHTTPRequestHandler):
    cfg = None

    def log_message(self, *a):
        pass

    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        c = self.cfg
        if not self.path.endswith("/chat/completions"):
            resp, code = post(base(c.fast) + self.path, raw)
            return self._send(resp, code)

        req = json.loads(raw)
        fast_req = dict(req, model=c.fast_model)
        resp, code = post(base(c.fast) + self.path,
                          json.dumps(fast_req).encode())

        route = "fast"
        try:
            content = json.loads(resp)["choices"][0]["message"].get("content") or ""
            is_verdict, parsed = wants_verdict(content)
            if is_verdict or not parsed:
                route = "judge" if parsed else "judge_unparseable"
        except (ValueError, KeyError, IndexError):
            route = "judge_unparseable"

        if route != "fast":
            judge_req = dict(req, model=c.judge_model)
            resp, code = post(base(c.judge) + self.path,
                              json.dumps(judge_req).encode())

        with LOCK:
            STATS[route] += 1
        self._send(resp, code)

    def do_GET(self):
        req = urllib.request.Request(base(self.cfg.fast) + self.path)
        try:
            with urllib.request.urlopen(req, timeout=60) as r:
                resp, code = r.read(), r.status
        except urllib.error.HTTPError as e:
            resp, code = e.read(), e.code
        self._send(resp, code)

    def _send(self, resp, code):
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(resp)))
        self.end_headers()
        self.wfile.write(resp)


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--listen", type=int, default=8100)
    ap.add_argument("--fast", required=True)
    ap.add_argument("--fast-model", required=True)
    ap.add_argument("--judge", required=True)
    ap.add_argument("--judge-model", required=True)
    ap.add_argument("--stats", default="")
    a = ap.parse_args()
    Handler.cfg = a
    print(f"split proxy :{a.listen}  actions -> {a.fast_model}  verdicts -> {a.judge_model}",
          flush=True)
    srv = ThreadingHTTPServer(("127.0.0.1", a.listen), Handler)
    try:
        srv.serve_forever()
    finally:
        if a.stats:
            open(a.stats, "w").write(json.dumps(STATS))
        print("routing:", STATS, flush=True)
