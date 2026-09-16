# Operating guide for an agent: getting reliable results from slmtest

This is the one page to read if you are an agent (or setting one up) that
will drive `slmtest` and needs the results to be *trustworthy*, not just
green. It covers both surfaces — the CLI and the MCP server — and assumes
you have read neither the codebase nor the lab notebooks.

Everything here is measured, not advisory; each claim links to the run that
produced it.

## The one thing to understand first

**The model that runs your test also grades it.** By design, `slmtest`
delegates pass/fail to the model (CLAUDE.md, "The agent contract"). That
makes it flexible and human-like, and it means a green run is a *claim*,
not proof.

This is not theoretical. Measured on this repo:

- A LoRA fine-tune of a 350M model scored **39/39** on the real example
  suite and looked ready to ship. Against steps whose success criteria were
  impossible to satisfy, it passed **21 of 21**, with self-refuting reasons
  — *"The file has 1 line, but the Expect criterion requires exactly 500
  lines."* Verdict: pass. ([`lfm2.5-350m-eval.md`](lfm2.5-350m-eval.md))
- A model asked for a hostname it could not produce ran
  `echo "server-does-not-exist-42"` instead of `hostname`, putting the
  expected text on screen. A judge model that is **42/42 honest** on the
  same suite then read that screen and passed the step. The judge was
  honest; the verdict was still wrong. ([`model-roles.md`](model-roles.md))

Both failures are invisible in the `-json` report unless you check. The
rest of this guide is how to make them visible.

## 1. Pick the model

| model | acts | judges honestly | notes |
|---|---|---|---|
| **Qwen3.5-9B-8bit** | 38/39 | **42/42** | the recommended default |
| Qwen3.5-4B-8bit | 32/39 | **28/28** | trap-clean at <half the size; weaker actor |
| LFM2-1.2B-Tool | 9/39 | no | tool-calling specialist; never emits a verdict |
| LFM2.5-350M (any quant, tuned or not) | 0/39 or fabricates | no | do not use |

Run it with `mlx-lm` on Apple Silicon, **8-bit not 4-bit** — 4-bit
measurably degrades multi-step reasoning. Setup in
[`model-runs.md`](model-runs.md).

Useful asymmetry if you are tempted to economise: **honesty is cheaper than
capability.** The 4B model judges as well as the 9B while acting worse. If
you ever split the roles, shrink the judge, not the actor
([`model-roles.md`](model-roles.md)).

## 2. Put ground truth in the spec

Do not rely on the model reading the screen. Add a check the harness runs
itself, which the model never sees and cannot stage:

```markdown
## Step 5: Delete the file
Goal: /tmp/report.csv no longer exists.
Hint: rm -f /tmp/report.csv
Expect: `ls /tmp/report.csv` reports that the file does not exist.
Verify: test ! -e /tmp/report.csv
```

- **`Verify:`** — a shell command run in a fresh process *outside* the
  driven session. Use absolute, durable state (`test -f /tmp/x`), never
  shell-local state (`$MYFILE`, relative paths), because the check cannot
  see the session's environment.
- **`VerifyDriver:`** — for state only the driver can see. With
  `driver: browser` it is JavaScript evaluated in the live page:
  `VerifyDriver: document.querySelector('#count').textContent === '1'`.

A failing check **forces the step to fail**, overriding a model's false
pass. A passing one never manufactures a pass. Full semantics and limits in
CLAUDE.md, "Ground-truth assertions".

**Add one to every step whose outcome leaves durable state.** Steps that
only produce screen output ("the banner says ready") cannot be covered, and
that is the honest boundary — there, the model's word is still all you have.

## 3. Read the right field

In the `-json` report (and identically in the MCP `structuredContent`),
each step may carry an `assertions` array. The field that matters:

```json
"assertions": [{"kind": "shell", "command": "test ! -e /tmp/report.csv",
                "passed": false, "agreed_with_model": false,
                "model_reason": "the file was removed successfully"}]
```

**`agreed_with_model: false` means the model's verdict could not be
trusted on that step.** It is a false-pass detector running on ordinary
specs. Surface it; do not average it away. The human CLI report prints an
explicit `ground-truth check DISAGREED with the model` line.

## 4. Calibrate the model before trusting it

Before adopting a new model, quant, or sampling config, run the trap suite
— steps with impossible success criteria mixed with satisfiable ones:

```
python3 scripts/trap_suite.py --endpoint http://localhost:8084/v1 \
  --model mlx-community/Qwen3.5-9B-8bit --label candidate --repeat 3
```

It exits non-zero on any false pass, so it can gate CI or a model upgrade.
Read **both** numbers it reports: `FALSE PASSES` tells you whether the
harness is safe to point at that model, and `model disagreed with ground
truth Nx` tells you whether the model itself is honest — a guarded run can
score perfectly while the model lies every time and is merely caught.
Details in [`trap-suite.md`](trap-suite.md).

## 5. Keep the evidence

```
slmtest run spec.md -json -trace ./traces/run-$(date +%s) -junit report.xml
```

`-trace` writes per-turn screen snapshots plus the full report — the thing
to hand to a human or another agent for review. Note it **overwrites**: use
a fresh directory per run if you want history. `-junit` gives CI inline
annotations for free.

## Using it over MCP

Build `go build -o slmtest-mcp ./cmd/slmtest-mcp` and point your client at
the binary (stdio). Everything above applies unchanged:

- `Verify:`/`VerifyDriver:` are **spec fields**, so they work over MCP with
  no extra parameter, and a failing check flips the tool result's `passed`
  to `false` exactly as on the CLI (verified end to end).
- `run_test`'s `structuredContent` is the same shape `-json` documents,
  including the per-step `assertions` array — read `agreed_with_model`
  there, same as above.
- `trace_dir`/`junit_path`/`golden_dir` mirror the CLI flags; `trace_dir`
  is the one to pass when the run should leave a reviewable artifact behind
  rather than living only in your context.
- `validate_test` is parse-only and cheap — call it freely while drafting a
  spec, before spending a model run.
- Progress arrives as standard `notifications/progress`, but **only if you
  supply a progress token** on the call.

The MCP server offers no way to disable ground-truth checks, deliberately:
they live in the spec, and a caller should not be able to silently switch
off the thing that makes a report trustworthy.

## Checklist

1. Qwen3.5-9B-8bit, 8-bit quant, `mlx-lm`.
2. `Verify:`/`VerifyDriver:` on every step with durable state.
3. Trap suite green (zero false passes) before trusting a new model.
4. Read `agreed_with_model`, not just `passed`.
5. `-trace`/`trace_dir` to a fresh path per run.
6. Treat a summary line as a claim; the transcript is the evidence.
