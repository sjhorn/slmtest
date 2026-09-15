#!/usr/bin/env python3
"""Run the slmtest example specs against one model endpoint and record,
per spec and per step, whether the model produced a usable action, whether
the step passed, and what it cost in latency and server memory.

Deliberately a thin wrapper over `slmtest run -json`: the harness does not
re-implement any judgement, it only aggregates the report the CLI already
emits (see CLAUDE.md, "The -json report shape"). That keeps a model
comparison honest — both models are graded by the exact same runner.

Usage:
  python3 scripts/model_eval.py --label lfm --model <hub-id> \
      --endpoint http://localhost:8085/v1 --server-pid <pid> --out eval-out
"""
import argparse, json, os, re, subprocess, sys, threading, time
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent

# bucket -> specs. `url` names an examples/*.html fixture that the spec
# needs passed in at run time (frontmatter cannot do path expansion).
SPECS = [
    # bucket,            spec,                              driver,  url fixture
    ("terminal",         "echo-test.md",                    "tui",   None),
    ("terminal",         "workspace-test.md",               "tui",   None),
    ("ui-interaction",   "browser-test.md",                 "browser", "browser-counter.html"),
    ("ui-interaction",   "browser-mouse-test.md",           "browser", "browser-mouse.html"),
    ("web-navigation",   "browser-form-test.md",            "browser", "browser-contact-form.html"),
    ("web-navigation",   "login-flow-test.md",              "browser", "login-flow.html"),
    ("multi-step",       "tui-editor-test.md",              "tui",   None),
    ("multi-step",       "nano-edit-test.md",               "tui",   None),
    ("multi-step",       "task-board-test.md",              "browser", "task-board.html"),
]


class RSSSampler(threading.Thread):
    """Peak RSS of the model server process, sampled while a spec runs.

    Sampling `ps` is coarse but it is the honest number for 'how much
    memory does standing this model up cost' — MLX holds weights resident,
    so the peak is dominated by the load, not by any one request."""

    def __init__(self, pid, interval=1.0):
        super().__init__(daemon=True)
        self.pid, self.interval, self.peak_kb, self._halt = pid, interval, 0, False

    def run(self):
        while not self._halt:
            try:
                out = subprocess.run(["ps", "-o", "rss=", "-p", str(self.pid)],
                                     capture_output=True, text=True).stdout.strip()
                if out:
                    self.peak_kb = max(self.peak_kb, int(out))
            except (ValueError, subprocess.SubprocessError):
                pass
            time.sleep(self.interval)

    def stop(self):
        self._halt = True
        self.join(timeout=3)
        return self.peak_kb


def doom_loop(text, window=20, min_repeats=2):
    """Detect the vendor-documented 'doom looping' failure mode: the same
    ~20-token window emitted repeatedly inside one reply. Reported, not
    acted on — the harness classifies, the runner still owns control flow."""
    toks = re.findall(r"\S+", text or "")
    if len(toks) < window * (min_repeats + 1):
        return False
    for i in range(len(toks) - window):
        w = " ".join(toks[i:i + window])
        if " ".join(toks).count(w) > min_repeats:
            return True
    return False


def analyse(report):
    """Collapse one run's report into the per-case numbers we compare on."""
    steps, turns_total, turns_valid, parse_errors, loops, turn_ms = [], 0, 0, 0, 0, []
    for s in report.get("steps", []):
        steps.append({"index": s.get("index"), "title": s.get("title"),
                      "status": s.get("status"), "reason": s.get("reason"),
                      "turns": s.get("turns"), "duration_ms": s.get("duration_ms")})
        for t in s.get("transcript", []):
            turns_total += 1
            if t.get("action"):
                turns_valid += 1
            if t.get("error"):
                parse_errors += 1
            if doom_loop(t.get("raw_reply", "")):
                loops += 1
            if t.get("duration_ms"):
                turn_ms.append(t["duration_ms"])
    turn_ms.sort()
    return {
        "passed": report.get("passed"), "aborted": report.get("aborted"),
        "duration_ms": report.get("duration_ms"),
        "steps": steps,
        "steps_total": len(steps),
        "steps_passed": sum(1 for s in steps if s["status"] == "pass"),
        "turns_total": turns_total, "turns_valid_action": turns_valid,
        "parse_errors": parse_errors, "doom_loops": loops,
        "turn_ms_median": turn_ms[len(turn_ms) // 2] if turn_ms else None,
        "turn_ms_mean": round(sum(turn_ms) / len(turn_ms)) if turn_ms else None,
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--label", required=True)
    ap.add_argument("--model", required=True)
    ap.add_argument("--endpoint", required=True)
    ap.add_argument("--server-pid", type=int, default=0)
    ap.add_argument("--out", default="eval-out")
    ap.add_argument("--request-timeout", default="3m")
    ap.add_argument("--step-timeout", default="240s")
    ap.add_argument("--buckets", default="")
    ap.add_argument("--only", default="", help="comma-separated spec filenames")
    ap.add_argument("--binary", default="slmtest",
                    help="binary to use for non-browser specs (a variant build, e.g. slmtest-shorttools)")
    ap.add_argument("--extra-args", default="",
                    help="extra slmtest flags, space-separated (e.g. -native-tools)")
    args = ap.parse_args()

    outdir = Path(args.out) / args.label
    outdir.mkdir(parents=True, exist_ok=True)
    want_b = set(filter(None, args.buckets.split(",")))
    want_s = set(filter(None, args.only.split(",")))

    results = []
    for bucket, spec, driver, fixture in SPECS:
        if want_b and bucket not in want_b:
            continue
        if want_s and spec not in want_s:
            continue
        binary = REPO / ("slmtest-browser" if driver == "browser" else args.binary)
        cmd = [str(binary), "run", str(REPO / "examples" / spec),
               "-endpoint", args.endpoint, "-model", args.model, "-json",
               "-request-timeout", args.request_timeout,
               "-step-timeout", args.step_timeout,
               "-continue-on-fail"]  # every step graded, not just up to the first failure
        cmd += [a for a in args.extra_args.split() if a]
        if fixture:
            cmd += ["-driver-option", f"url=file://{REPO / 'examples' / fixture}"]
        print(f"[{args.label}] {bucket:16} {spec}", file=sys.stderr, flush=True)

        sampler = RSSSampler(args.server_pid) if args.server_pid else None
        if sampler:
            sampler.start()
        t0 = time.time()
        proc = subprocess.run(cmd, capture_output=True, text=True, cwd=REPO)
        wall = time.time() - t0
        peak_kb = sampler.stop() if sampler else 0

        try:
            report = json.loads(proc.stdout)
        except json.JSONDecodeError:
            report = None
        entry = {"bucket": bucket, "spec": spec, "driver": driver,
                 "exit_code": proc.returncode, "wall_s": round(wall, 1),
                 "peak_rss_mb": round(peak_kb / 1024, 1) if peak_kb else None}
        if report is None:
            entry["harness_error"] = (proc.stderr or proc.stdout)[-2000:]
        else:
            (outdir / f"{spec}.json").write_text(json.dumps(report, indent=2))
            entry.update(analyse(report))
        results.append(entry)
        print(json.dumps({k: entry.get(k) for k in
                          ("spec", "steps_passed", "steps_total", "turns_valid_action",
                           "turns_total", "doom_loops", "wall_s", "peak_rss_mb")}),
              file=sys.stderr, flush=True)

    (Path(args.out) / f"{args.label}-summary.json").write_text(
        json.dumps({"label": args.label, "model": args.model,
                    "endpoint": args.endpoint, "results": results}, indent=2))
    print(f"wrote {Path(args.out) / (args.label + '-summary.json')}", file=sys.stderr)


if __name__ == "__main__":
    main()
