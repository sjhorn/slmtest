#!/usr/bin/env python3
"""Measure time-to-first-token and total latency for one representative
slmtest-shaped request.

slmtest itself never streams (internal/agent/client.go sends a plain
chat-completions request and reads message.content), so TTFT is invisible
to the reports model_eval.py aggregates. This probe streams the same shape
of request separately, purely to attribute LFM's latency between prefill
and decode."""
import argparse, json, time, urllib.request

PROMPT = ("You are driving a terminal. Reply with exactly one JSON object and nothing else.\n"
          "Step goal: create a file named hello.txt containing the word hi.\n"
          "Reply now with {\"action\": ..., \"command\": ...}.")


def probe(endpoint, model, n):
    ttfts, totals, samples = [], [], []
    for _ in range(n):
        body = json.dumps({
            "model": model, "stream": True, "temperature": 0.1, "max_tokens": 256,
            "messages": [{"role": "system", "content": "Reply with one JSON object."},
                         {"role": "user", "content": PROMPT}],
        }).encode()
        req = urllib.request.Request(endpoint.rstrip("/") + "/chat/completions", body,
                                     {"Content-Type": "application/json"})
        t0 = time.time()
        first, text = None, []
        with urllib.request.urlopen(req, timeout=300) as r:
            for raw in r:
                line = raw.decode().strip()
                if not line.startswith("data:"):
                    continue
                payload = line[5:].strip()
                if payload == "[DONE]":
                    break
                chunk = json.loads(payload)["choices"][0].get("delta", {}).get("content")
                if chunk:
                    if first is None:
                        first = time.time() - t0
                    text.append(chunk)
        ttfts.append(first or 0)
        totals.append(time.time() - t0)
        samples.append("".join(text))
    return {"ttft_ms_mean": round(1000 * sum(ttfts) / len(ttfts)),
            "total_ms_mean": round(1000 * sum(totals) / len(totals)),
            "sample_reply": samples[-1][:400]}


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--endpoint", required=True)
    ap.add_argument("--model", required=True)
    ap.add_argument("-n", type=int, default=3)
    a = ap.parse_args()
    print(json.dumps(probe(a.endpoint, a.model, a.n), indent=2))
