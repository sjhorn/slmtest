# Running against real models, and what it has found

This file is two things: a runbook for reproducing real-model testing
yourself, and a running log of what those runs have found. Update the log
whenever you run a new model or spec against the harness — that is the
whole point of keeping it separate from `CLAUDE.md`, which stays a stable
reference rather than a growing lab notebook.

See [`CLAUDE.md`](../CLAUDE.md) for the spec format, the agent contract,
and how the harness itself works. See "CI runs no model" there for why
none of this runs in GitHub Actions.

## Why this matters more than it sounds like it should

Everything in the Go test suite is verified against
`examples/mock_slm_server.py`, a deterministic stand-in that answers
identically no matter what it is asked. It proves the harness's plumbing
works. It cannot prove the harness works *against a model*, because a
mock cannot misuse the action schema in a way a real model might.

Every defect logged below was invisible to the mock and to unit tests,
and several were invisible to a large model too. The smaller and weaker
the model, the more of the robustness machinery (parse-error feedback,
turn budgets, nudges) actually gets exercised — which means testing only
against a capable model tells you almost nothing about whether that
machinery works.

## How to run this yourself

The harness only speaks OpenAI-compatible HTTP, so "which model" is just
what you point `-endpoint` at.

### A small model, locally, via llama.cpp

`llama-server` is one binary, needs no daemon, and fetches the weights
itself the first time you run it. Check it's current before trusting a
surprising result — see "Ruling out a stale inference engine" below; an
install here was found six months stale and nothing else in this file
would have caught that on its own:

```
brew upgrade llama.cpp    # or however you installed it
llama-server --version    # compare the build number against
                          # https://github.com/ggml-org/llama.cpp/releases/latest
```

Then:

```
llama-server -hf Qwen/Qwen2.5-1.5B-Instruct-GGUF:Q4_K_M --port 8080 -c 8192
llama-server -hf Qwen/Qwen2.5-0.5B-Instruct-GGUF:Q4_K_M --port 8081 -c 16384  # a weaker model needs more headroom, not less — see the context-overflow note below
```

Then, from the repo root:

```
go build -o slmtest ./cmd/slmtest
./slmtest run examples/echo-test.md       -endpoint http://localhost:8080/v1 -request-timeout 3m
./slmtest run examples/workspace-test.md  -endpoint http://localhost:8080/v1 -request-timeout 3m -continue-on-fail
./slmtest run examples/tui-editor-test.md -endpoint http://localhost:8080/v1 -request-timeout 3m -continue-on-fail
```

`-continue-on-fail` matters for the multi-step specs: without it, the run
stops at the first failing step, and the whole point of these runs is
seeing what a weak model gets wrong further in. Add `-verbose` to watch
each turn live, or `-json > out.json` and inspect the transcript
afterward — the transcript is where a false pass (see below) actually
shows up; the one-line summary will not tell you.

There are deliberately **no llama.cpp bindings** in this repo — see
"No llama.cpp bindings" below.

### A large model, as a quality reference

If a small model fails a step, running the same spec against a stronger
model tells you whether the spec is ambiguous or the small model is the
limit:

```
./slmtest run examples/workspace-test.md -endpoint http://localhost:8888/v1 -model <name>
```

(`:8888` above is whatever OpenAI-compatible endpoint you already have —
a local vLLM instance, LM Studio, Ollama's compat mode, or a hosted API.)

### What to run after changing the runner, schema, or PTY handling

CI does not cover this (again, see "CI runs no model" in `CLAUDE.md`), so
it is manual. At minimum:

```
./slmtest run examples/echo-test.md       -endpoint <a small local model> -request-timeout 3m
./slmtest run examples/tui-editor-test.md -endpoint <a small local model> -request-timeout 3m -continue-on-fail
```

`tui-editor-test.md` is the higher-value check: it exercises `send_keys`,
control characters, and multi-step state in a way `echo-test.md` cannot,
and it is where the sharpest defects (below) were actually found. Read
the transcript, not just the summary line — see "The false-pass problem"
below for why the summary alone is not enough.

### No llama.cpp bindings

A cgo binding to llama.cpp was considered and rejected. It would bridge
nothing that HTTP does not already cover — `llama-server` already speaks
OpenAI-compatible chat completions — while costing a C++ toolchain in the
build, platform-specific acceleration flags, and a CI matrix that
currently just works. If launching the server ever needs to be automatic,
the honest form is a small `os/exec` supervisor around `llama-server`,
not a linked library.

## The false-pass problem

Read this before trusting any green result below.

The harness deliberately never infers pass/fail itself — that is what
makes it a QA harness rather than an assertion runner, and it is a
foundational design choice, not a bug. The cost is that **a model owns
the verdict, so a model willing to assert an unearned pass will
produce one**, and this has been observed more than once below. No flag
fixes this; requiring a passing step to "cite output" was considered and
rejected, because judging whether cited output actually *supports* the
Expect criterion is the exact judgement being delegated in the first
place.

The mitigation is procedural, not mechanical: **treat a summary line as a
claim and the transcript as the evidence.** `-json` carries the full
transcript for exactly this reason. The single cheapest defence when
*writing* a spec is to end it with a step that checks ground truth (read
a file back, `cat` it, check an exit code) rather than a step whose
Expect can be satisfied by the terminal's own echo or a TUI's redraw — a
weak model can talk itself past the latter and cannot fake the former.

## Findings log

### Against a large model (vLLM, DeepSeek-V4-Flash)

`workspace-test.md` passed 5/5 under `-sandbox`, including the step that
asks the model to notice a write was refused. Replies parsed first time,
no code fence.

**Found:** a 60s request timeout that a large-context endpoint could
legitimately exceed, made worse by the retry ladder spending three
minutes rediscovering it before aborting. Fixed by making the timeout
configurable (`-request-timeout`, default raised to 2m).

A large model getting it right first time is *not* evidence the
small-model machinery (fence-stripping, parse-error feedback, nudges)
works — it is evidence it wasn't needed.

### Against a very small model (llama.cpp, Qwen2.5-0.5B-Instruct Q4_K_M)

Below the capability floor for this task, and that is what made it
valuable: it found the worst bug in the harness so far, and the harness
had introduced the bug itself.

It "passed" `echo-test.md` in two turns — falsely. It used `send_keys`,
which does not press Enter, so `echo hello-from-pty` was typed and never
executed. The marker string was genuinely on screen, as the terminal
echoing its own input, and it declared pass on that. The harness reported
PASS for a step where nothing ran.

**And a harness nudge had coached it there.** `strayVerdictNote`
interpolated the model's own claimed `step_result` into the reply it
suggested sending, so a model that had written `"pass"` was told, in
effect, "reply with pass". `repeatNudge` had the same lean. That went
unnoticed because it helped the 1.5B, which happened to be right, and
only manufactured a false pass against a model weak enough to be wrong.

**Fixed two ways:**

- **No nudge may ever name a verdict.** They now offer `"pass" or "fail"`
  together and say to judge from the output.
  `TestNudgesNeverSupplyAVerdict` enforces that any note naming one
  verdict names both.
- **`notExecutedNote`** states the mechanical fact after a `send_keys`
  with no Enter: the text was typed, nothing ran, and anything on screen
  matching what was typed is the echo, not output.

With both fixes, the 0.5B honestly fails `echo-test.md` — it alternates
`send_keys` and `run_command` and never reaches a verdict — and the 1.5B
still passes in two turns. Converting a false pass into an honest failure
is the improvement; a harness that cannot fail is worthless.

**The general lesson:** a nudge that mentions one verdict is the harness
voting. Anything the harness adds to a prompt must describe mechanism,
never conclusion.

### Against a small model (llama.cpp, Qwen2.5-1.5B-Instruct Q4_K_M)

The regime this tool is designed for. Found three real defects in a
single one-step test, all invisible to the mock and the large model.

**1. `press_enter: false` on `run_command` was a silent no-op.** The
model set the field, the harness honored it, so the command was typed but
never executed. No output ever appeared, and the model spent its entire
turn budget waiting for a result that could not arrive. `run_command` is
*defined* as "type and press Enter", so the field is now ignored there
and honored only for `send_keys`. A field whose misuse produces silence
rather than an error is a design bug, and only a model naive enough to
misuse it would ever reveal that.

**2. It repeated an identical correct command against unchanged output.**
The Expect criterion was plainly satisfied in front of it and it never
drew the conclusion. `repeatNudge` now points this out from the second
repeat.

**3. It attached `"step_result": "pass"` to `run_command`** — right
judgement, wrong mechanics — while never calling `finish_step`.

Point 3 is the instructive one: **the first fix made things strictly
worse.** Rejecting the stray field as a schema error looked principled
and produced a rejection loop — the model resent the same malformed
action every turn, so no command ran at all and the entire budget went on
the argument. Tolerating it and appending a note naming the mistake, with
the exact corrected reply, turned a 4-turn failure into a 2-turn pass.
(That echoed-reply wording was itself later found to bias the model — see
the 0.5B section above — and was replaced with wording that names both
verdicts.)

With all three fixed, the 1.5B passes `echo-test.md` in 2 turns and
`workspace-test.md` 5/5 under `-sandbox` — **but do not read that 5/5 as
clean.** Step 2 passed *without verifying anything*: the model ran the
write command, never ran the `cat` its own Expect line called for, and
gave as its reason a sentence lifted verbatim from the prompt's
boilerplate. It reached the right verdict by luck, not by reading output.
See "The false-pass problem" above.

Later, against `tui-editor-test.md`, the 1.5B produced **three
consecutive false passes**: it sent `i\n` (entering insert mode, then a
stray newline) and never typed the required text; its "save and quit"
was `wq` sent in normal mode, which vi rejected with a bell. Both were
reported as confident passes. The run still reported FAIL overall,
because the final content-check step ran `cat` against the real file,
found nothing, and correctly failed — the only step in the run that
checked ground truth rather than the screen.

### A second fresh run (0.5B, 1.5B, and the large model, same day)

Re-run to validate the false-pass fixes above and to broaden the sample.
Two new failure modes surfaced, neither seen in the first pass.

**Parse-error feedback does not reliably work at 0.5B.** The core design
principle — feed a schema error back and the model self-corrects — failed
outright here. On `tui-editor-test.md`, the 0.5B sent
`{"action":"finish_step","reason":"..."}` with no `step_result`, got back
the exact validation error, and **sent the byte-for-byte identical reply
three more times**, verbatim, before giving up. It never varied the
retry. This is the sharpest limit found yet on "one design choice matters
more than any prompt wording": that only holds once a model is capable
enough to act on the correction, and 0.5B is below that line.

**A model can abuse `abort_test` to escape a loop it cannot get out of.**
After the repeated schema-error loop above, the 0.5B's next reply was
`{"action":"abort_test","reason":"The test has been completed."}` — a
plainly false claim (the step had not passed, and nothing indicated the
environment was broken). `abort_test` is reserved for a genuinely
unusable environment; here it was used as an exit from confusion. This
matters because it pollutes the one signal that is supposed to mean "stop
everything, this run cannot continue" — a real environment failure and a
stuck weak model now look identical in the report. Not yet fixed; see
"Known gaps" in `CLAUDE.md`.

**A `finish_step` can carry a hallucinated, never-run command.** On
`workspace-test.md`, the 1.5B's cleanup step sent
`{"action":"finish_step","command":"ls -d /tmp/slmtest-workspace", ...,
"step_result":"fail","reason":"The command failed, indicating the path no
longer exists."}` — `finish_step` does not execute `command` at all, so
`ls` never ran. The model reasoned from a command it imagined running,
and then **inverted the verdict**: "the path no longer exists" is exactly
what a successful `rm -rf` produces and exactly what the step's own
Expect line asked for, yet it was scored `fail`. This is the mirror image
of the earlier false-pass problem — a false *fail*, reached by treating
"reports non-existence" as "the command failed" rather than reading what
the Expect criterion actually wanted. The same run also produced a
confidently-worded false negative on the sandbox-detection step, from a
command whose own quoting bug (`echo 'exit=$?'`, single quotes suppressing
the substitution) made the true exit code impossible to read from the
output at all — the model asserted "failed as expected" from output that
could not have told it that.

**Re-running the same spec against the same model gives different
failures, not just different pass/fail.** The first `tui-editor-test.md`
run against the 1.5B failed via three false passes on steps 2–4. The
second run passed steps 1, 3, 4 and instead exhausted its entire turn
budget on step 2 — having correctly entered insert mode and typed the
text in turn 1, it then tried to "verify" by running `run_command` with
`echo 'tui input works'` seven times in a row, each one actually just
inserting more literal text into the vi buffer it was still inside of,
since vi consumes keystrokes rather than a shell. `repeatNudge` fired
from the second repeat and named the wrong action explicitly — the model
kept resending near-identical `run_command` calls anyway. The nudge
changed nothing here, in contrast to the 0.5B `echo-test.md` case where
an equivalent nudge did cause a change of approach. **A model this size
cannot reliably track "I am still inside a modal program that consumes
my run_command as buffer text, not as a shell command"** — a materially
different confusion from anything in the first run, on the identical spec
and endpoint.

An operational note from the same session, not a harness bug: running the
0.5B against `workspace-test.md` with `-c 8192` on the `llama-server` side
hit `request (8229 tokens) exceeds the available context size` mid-step,
after several malformed-JSON retries had accumulated in that step's
history. Per-step history resets between steps, but *within* one step a
long enough retry sequence can still overflow a small context window.
Raise `-c` on `llama-server` when running a weak model through
`-continue-on-fail` across multi-step specs.

A second operational note, from the large-model leg of this same run: the
endpoint hung on its completions path for several minutes — `/v1/models`
kept answering instantly while a bare, minimal completion request timed
out with zero bytes — then recovered on its own with no restart. `-json`
correctly reported `abort` with `SLM endpoint error: ... context deadline
exceeded`, distinct from a step failure, which is exactly the distinction
`abort` exists to preserve: this was never a claim about the system under
test. Re-running the same specs a few minutes later against the same
endpoint passed cleanly across the board — `echo-test.md` (2 turns),
`workspace-test.md` 5/5, `tui-editor-test.md` 6/6 genuinely verified, and
`tui-claude-test.md` 6/6 on both the original and the repeat run. If a run
aborts on a transport error against an endpoint you don't control,
checking whether it has simply recovered is worth doing before assuming
the harness is at fault.

`workspace-test.md` step 4 is worth calling out on this clean run: no
`-sandbox` flag was set, the write succeeded, and the model correctly
reported `exit=0` and a pass — the other half of the dual-mode step
design working as intended, not just the sandboxed half seen in earlier
sessions.

### Against Claude Code's own TUI (large model only, `tui-claude-test.md`)

Deliberately scoped to the trust prompt shown for an unfamiliar folder —
never sends a message, so it costs no tokens and stays deterministic. The
large model passed 6/6: read the two-option menu off the rendered screen,
declined with Esc, and left no project entry or session behind (verified
after the run by checking `~/.claude.json` and the filesystem).

## Results summary

| Spec | 0.5B | 1.5B | Large model |
|---|---|---|---|
| `echo-test.md` | fail (honest, after the fix) | pass, 2 turns | pass, 2 turns |
| `workspace-test.md` | fail — repeated-reply loop, then context overflow | fail — false-fail from a hallucinated command; see below | pass 5/5 |
| `tui-editor-test.md` | fail — repeated-reply loop escalated to a false `abort_test` | fail both runs, by two different failure modes each time — see below | pass 6/6, genuinely verified |
| `tui-claude-test.md` | not yet run | not yet run | pass 6/6 (both runs) |

Treat capability at the 1.5B size as "can drive a shell", not "can judge
a screen" — it is capable enough to produce confident, well-worded,
entirely false verdicts about a TUI it has misread.

## Ruling out a stale inference engine

After the findings above, the installed `llama.cpp` (via Homebrew) turned
out to be six months old: build `8110` (commit `237958db3`, 2026-02-19)
against upstream's latest tagged release `v0.3.0` (build `10621`, commit
`c1d0e7a00`, published 2026-08-25) — a ~2,500-build gap. Worth checking
before trusting any finding above attributed to "the model": an old
inference engine could plausibly account for malformed JSON, timeouts, or
sampling oddities that look like model behavior but aren't.

`brew upgrade llama.cpp`, confirmed via `llama-server --version`, then
re-ran the two cases most central to the findings above, against fresh
`llama-server` instances on the new build:

- **`echo-test.md` on both sizes** — 1.5B passed cleanly in 2 turns, 0.5B
  failed honestly by alternating `send_keys`/`run_command` and never
  reaching a verdict. Identical shape to the pre-upgrade result.
- **`tui-editor-test.md` on the 0.5B** (raised to `-c 16384` at the same
  time, since this is also where the earlier context-overflow note came
  from) — reproduced the non-self-correcting retry loop exactly: given
  the same schema error eight turns in a row, it sent the
  **byte-for-byte identical malformed reply eight times**, then misused
  `abort_test` again ("The test is now considered failed" — as false a
  claim as the first session's). It also invented a new nonexistent
  action, `open_file`, not seen in the original session — further
  evidence of the general pattern rather than a new class of bug. The
  context-overflow error did not recur at the larger `-c`.

**Conclusion: none of the findings in this log are an artifact of the old
`llama.cpp` build.** The specific bytes differ turn to turn (expected —
these are sampled, non-deterministic replies) but the failure shapes —
non-self-correction under repeated identical schema errors, misuse of
`abort_test` to escape a stuck loop, confident wrong verdicts — reproduce
on current `llama.cpp`. They are model-capability findings, not
inference-engine bugs. Keep `llama.cpp` current regardless: an outdated
engine is still a plausible confound for anything unusual observed in
future runs, and this check is cheap enough to repeat before trusting a
surprising result — `brew outdated | grep llama.cpp`, or compare
`llama-server --version`'s build number against
[the latest release](https://github.com/ggml-org/llama.cpp/releases/latest).

## Sampling caveat

This log currently covers one model family (Qwen2.5), one local runtime
(llama.cpp), two small sizes, and four specs, with no repeated runs. Every
fix above is a fix for the specific failure mode one specific model
happened to hit. The principles extracted (tolerate and state the
correction rather than reject; never let a nudge name a verdict; end
specs with a ground-truth step) are believed to generalize, but that is a
belief, not something this log has tested at scale. Widening the sample —
other model families, other quantisations, repeated runs of the same
spec — is the natural way to stress-test that belief.
