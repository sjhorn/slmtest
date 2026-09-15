#!/usr/bin/env python3
"""Turn logged Qwen traces into an mlx-lm LoRA chat dataset.

Only turns belonging to steps that actually PASSED are kept. Training on a
failed step's turns would teach the small model the flailing as well as the
fix, and the whole premise of a narrow fine-tune here is to transfer the
*envelope* — correct action names, correct params nesting, and above all
actually calling finish_step — not to transfer Qwen's mistakes.

Emits train/valid/test.jsonl in the {"messages": [...]} chat format mlx-lm
reads directly, with the assistant turn last.
"""
import argparse, collections, glob, json, pathlib, random


def passing_pairs(report_glob):
    """(user_prompt, raw_reply) pairs from passing steps only.

    Handles both an ordinary report and a Feature report ({"scenarios": [...]}),
    since both shapes are in eval-out/."""
    keep = set()
    for f in glob.glob(report_glob, recursive=True):
        try:
            rep = json.load(open(f))
        except (ValueError, OSError):
            continue
        reports = rep.get("scenarios") or [rep]
        for r in reports:
            for s in r.get("steps", []):
                # A passing step is good behaviour throughout. A FAILING step is
                # good behaviour too, but only when the model chose to fail it
                # deliberately — that is a correctly-reasoned negative verdict,
                # and without some of those the dataset teaches "always pass",
                # which is the exact false-pass failure this eval already caught
                # the base model committing. A step that merely ran out of turns
                # is excluded: nothing there is worth imitating.
                if s.get("status") == "pass":
                    pass
                elif s.get("status") == "fail" and any(
                        (t.get("action") or {}).get("action") == "finish_step"
                        for t in s.get("transcript", [])):
                    pass
                else:
                    continue
                for t in s.get("transcript", []):
                    if t.get("raw_reply") and not t.get("error"):
                        keep.add((t.get("user_prompt", ""), t["raw_reply"]))
    return keep


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--traces", default="traces/qwen-traces.jsonl")
    ap.add_argument("--reports", default="eval-out/**/*.json")  # needs recursive=True
    ap.add_argument("--out", default="traces/data")
    ap.add_argument("--valid-frac", type=float, default=0.1)
    ap.add_argument("--test-frac", type=float, default=0.05)
    ap.add_argument("--seed", type=int, default=0)
    args = ap.parse_args()

    keep = passing_pairs(args.reports)
    print(f"{len(keep)} distinct (prompt, reply) pairs from passing steps")

    seen, rows, actions = set(), [], collections.Counter()
    verdicts = collections.Counter()
    for line in open(args.traces):
        try:
            rec = json.loads(line)
        except ValueError:
            continue
        msgs, reply = rec.get("messages") or [], rec.get("reply") or ""
        if not msgs or not reply:
            continue
        last_user = next((m["content"] for m in reversed(msgs)
                          if m.get("role") == "user"), "")
        if (last_user, reply) not in keep:
            continue
        key = json.dumps(msgs, sort_keys=True) + reply
        if key in seen:          # repeat sweeps at temp 0.1 produce near-clones
            continue
        seen.add(key)
        try:
            act = json.loads(reply.strip().removeprefix("```json").removesuffix("```").strip())
            actions[act.get("action")] += 1
            if act.get("action") == "finish_step":
                verdicts[act.get("step_result")] += 1
        except ValueError:
            pass
        rows.append({"messages": msgs + [{"role": "assistant", "content": reply}]})

    random.Random(args.seed).shuffle(rows)
    n = len(rows)
    n_valid, n_test = int(n * args.valid_frac), int(n * args.test_frac)
    splits = {"valid": rows[:n_valid],
              "test": rows[n_valid:n_valid + n_test],
              "train": rows[n_valid + n_test:]}

    out = pathlib.Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    for name, part in splits.items():
        with open(out / f"{name}.jsonl", "w") as f:
            for r in part:
                f.write(json.dumps(r) + "\n")
        print(f"  {name}: {len(part)}")
    print("action mix:", dict(actions.most_common()))
    print("finish_step verdicts:", dict(verdicts),
          "<-- watch this balance: a pass-only set teaches 'always pass'")


if __name__ == "__main__":
    main()
