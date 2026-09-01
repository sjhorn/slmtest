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

### A small model, locally, via mlx-lm (recommended on Apple Silicon)

On Apple Silicon, `mlx-lm` is now the recommended local setup for this
project — **~40% faster token generation than llama.cpp at equal or
better reliability**, provided you use an 8-bit (not 4-bit) quant. See
"mlx-lm vs llama.cpp" in the findings log below for the full
investigation; this section is just the setup.

```
uv venv --python 3.12 .venv
uv pip install --python .venv/bin/python mlx-lm
.venv/bin/python -m mlx_lm.server \
  --model mlx-community/Qwen3.5-9B-8bit \
  --chat-template-args '{"enable_thinking":false}' \
  --prompt-cache-size 8 \
  --port 8084
```

Two flags are load-bearing, not cosmetic:

- **`--chat-template-args '{"enable_thinking":false}'` is required.**
  Without it, Qwen3.5's thinking mode puts the model's actual answer in
  a `reasoning` field and leaves `content` empty (or truncated mid-
  thought at `mlx_lm.server`'s default `--max-tokens 512`) — `slmtest`
  only ever reads `message.content` (see `internal/agent/client.go`), so
  every turn would fail to parse.
- **`--prompt-cache-size 8`** lets the server reuse KV cache across
  requests that share a growing prefix — exactly `slmtest`'s per-step
  history shape (each turn's request is the previous one plus one more
  turn). Confirmed live: a follow-up request reusing 31 of 54 prompt
  tokens from cache. Free speedup, no flag-off downside.

Then, same as the llama.cpp path — `slmtest` just needs the model name,
since `mlx_lm.server`'s `/v1/models` lists every cached model rather than
only the one currently loaded (unlike `llama-server`):

```
go build -o slmtest ./cmd/slmtest
./slmtest run examples/echo-test.md -endpoint http://localhost:8084/v1 -model mlx-community/Qwen3.5-9B-8bit -request-timeout 3m
```

**Do not reach for the 4-bit quant to go faster** — it measurably
degrades reliability on multi-step reasoning tasks (see the findings log
entry below for two independent 4-bit MLX quants both failing the same
spec that 8-bit clears reliably).

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
| `tui-claude-test.md` | not run | pass 6/6, all clean 2-turn steps | pass 6/6 (both runs) |

Treat capability at the 1.5B size as "can drive a shell", not "can judge
a screen" — it is capable enough to produce confident, well-worded,
entirely false verdicts about a TUI it has misread. `tui-claude-test.md`
against the 1.5B is the exception worth noting: every step was a clean
2-turn pass, no verdict errors at all — reading a static, already-rendered
menu and declining with Esc turned out to be well within this size's
reach, unlike judging the *outcome* of a multi-step edit.

### Retest after the fixes in this session (fresh servers, default mode)

Re-run after upgrading `llama.cpp`, restarting both local servers fresh,
and implementing native tool-calling (see below) — specifically to check
whether "fixing how we talk to the model" actually moved results, not
just to repeat prior findings.

**Nothing regressed and nothing new broke.** 0.5B still fails every spec
honestly (no false passes anywhere) via the same known patterns — a
non-self-correcting retry loop, and the `abort_test` misuse this project
still hasn't fixed. 1.5B still passes `echo-test.md` cleanly and still
produces the same two recurring bug classes on the longer specs:

- **The hallucinated-`finish_step`-command false-fail reproduced again**,
  byte-for-byte the same shape as before: `workspace-test.md`'s cleanup
  step attached an unused `command: "ls -d ..."` to `finish_step` (never
  executed), reasoned as if it had run, and inverted the verdict — the
  same defect found earlier in this log, now confirmed as a *recurring*
  pattern rather than a one-off sample.
- **`tui-editor-test.md`** passed 4 of 6 steps this time, including the
  ground-truth `cat`-verification step genuinely passing — but step 2
  false-negatived (confused vi's insert mode with a search, hitting
  `E486: Pattern not found`) and step 6 repeated the same
  inverted-verdict pattern as above on a *different* step. Different
  specific mistake, same two bug classes, every run.

**`-native-tools` was also tested on `workspace-test.md` for the first
time (previously only `echo-test.md` had been checked), and the
regression generalizes**: 4 of 5 steps failed via turn-exhaustion — not
sporadically, but the model failing to produce a `finish_step` tool call
on step after step. This is stronger evidence than the single-spec
finding above that the second-tool-call degradation is not
`echo-test.md`-specific, and confirms `NativeTools` defaulting to off
was the right call, not an overreaction to one test.

**xLAM-2-1b-fc-r was retested too** (fresh server; it had been left out
of the first pass of this retest by mistake). Full suite, prose (default)
mode:

| Spec | Result |
|---|---|
| `echo-test.md` | pass, 3 turns — one schema violation (missing `reason`), self-corrected |
| `workspace-test.md` | fail — 3/5 genuine passes (incl. a real `wc -w` verification and a real, unsandboxed `touch` check), 2 lost to the comma-drop defect cascading into a non-self-correcting loop |
| `tui-editor-test.md` | fail — 2/6 pass, 4 lost the same way |
| `tui-claude-test.md` | fail — 2/6 pass, then the same loop from step 3 onward |

Two things confirmed as *recurring*, not one-off samples from the
original comparison: the missing-comma-after-`thought` defect
(`docs/model-runs.md`, above) reproduces again, and — new — once it
happens, xLAM does not reliably self-correct from the resulting schema
error, repeating the identical error 7–8 times in a row rather than
varying its retry, the same non-self-correcting pattern documented for
the 0.5B. xLAM sits, empirically, between the two Qwen sizes on this
axis: better than the 0.5B (its *first* schema violation in a step
usually does self-correct, as `echo-test.md` shows), worse than the
1.5B (it falls into the 0.5B's failure mode once a defect occurs).

**The `-native-tools` fallback was also re-verified against xLAM
specifically — the exact case it exists for.** It engaged correctly:
no crash, no hard abort, a clean fall-through to the prose path after
the tools-enabled attempts failed. The resulting run still failed, but
from xLAM's own pre-existing comma-drop defect in the prose path, not
from anything native-tools related — confirming the fallback mechanism
itself works, independent of whether the underlying model has other
problems.

## Does a tool-calling-tuned model do better than a general one?

Prompted by the findings above being dominated by JSON-schema mechanics
(malformed replies, wrong fields, invented action names), it's a fair
question whether a model specifically fine-tuned for reliable tool/
function calling — rather than a general instruct model that happens to
answer in JSON — does better inside this harness. Tried
[`Salesforce/xLAM-2-1b-fc-r`](https://huggingface.co/Salesforce/xLAM-2-1b-fc-r-gguf),
a 1.54B model (Qwen2 architecture, so directly size-comparable to the
1.5B above) purpose-trained for function calling, reporting
state-of-the-art results on BFCL and τ-bench.

**Caveat stated before testing, confirmed after:** those benchmark
numbers were measured using xLAM's own trained tool-definition prompt
template, not a generic English system prompt describing a JSON schema
in prose — which is how this harness deliberately talks to *any* model,
to stay endpoint-agnostic. The comparison run used our existing system
prompt completely unmodified, exactly as any real `slmtest` user would.

**Result: no improvement.** `echo-test.md` passed in 3 turns (vs. the
1.5B's 2) — one schema violation (a missing `reason` field), but it
**self-corrected correctly** on the retry, unlike Qwen's 0.5B which
looped on identical errors. `tui-editor-test.md` failed, 3 of 6 steps
lost to turn-exhaustion — roughly comparable overall reliability to the
two Qwen2.5-1.5B runs above, and no false pass this time (step 5's `cat`
verification genuinely passed).

**But the *failure signature* was new and distinctive**, and confirms the
caveat: across the two failed steps, **12 of 13 malformed replies were
missing exactly one comma** — between the `"thought"` field and the
`"action"` field, and nowhere else:

```
..."the model's stated reasoning."
  "action": "run_command", ...
```

This never appeared once in either Qwen model. The most plausible
explanation is the template mismatch: xLAM was very likely trained to
emit a reasoning segment and a tool-call block as two separate elements
with their own boundary (not a JSON comma), and asking it to fit both
into one flat object as sibling keys fights that learned structure.

**Conclusion:** "benchmark-strong at tool calling" does not reliably
transfer into a harness that talks to a model in a generic, endpoint-
agnostic way rather than the model's native format. This isn't a knock on
xLAM — it's a mismatch between what it was optimized for and how this
tool necessarily talks to models. It does suggest a model-selection rule
worth stating plainly: **for this harness, general instruction-following
quality at the target size predicts performance better than a
tool-calling-specific benchmark does**, precisely because the harness
never uses a model's native tool-calling template.

Two operational notes from getting this running, since the mechanics
almost derailed the comparison entirely:

- **`llama-server -hf`'s quant-tag matching is case-sensitive.**
  `Salesforce/xLAM-2-1b-fc-r-gguf:Q4_K_M` (lowercase `1b`, matching the
  repo name) silently hung rather than erroring, because the actual file
  is `xLAM-2-1B-fc-r-Q4_K_M.gguf` (capital `B`). Silent hangs are the
  worst failure mode here — there's no error to act on, just a process
  that never binds its port. If `-hf` hangs with no download-progress log
  line at all, check the repo's actual filenames
  (`curl https://huggingface.co/api/models/<repo>` lists them) before
  assuming a network problem.
- **This repo's storage backend (Hugging Face's newer "Xet" CDN) hung
  `llama-server`'s built-in downloader even with the exact filename
  given via `--hf-file`**, while the same URL fetched fine through a
  plain `curl`. Worked around by downloading directly and pointing
  `llama-server -m` at the local file. Worth trying first if `-hf` ever
  stalls with no visible error against a model whose files show a
  `xet-bridge` redirect target.

## Using the OpenAI `tools`/`tool_calls` API instead of a prose schema

The comma-drop defect above is a symptom of a deeper problem: describing
the action schema in the system prompt and parsing whatever text comes
back asks every model to freeform-generate JSON, when the OpenAI API
already has a purpose-built mechanism for exactly this —
`tools`/`tool_calls` — that most serving stacks, including current
`llama-server`, implement using the model's own native chat template.
Verified live against both models already in this log:

**Against Qwen2.5-1.5B, a single isolated call worked perfectly, no code
changes.** Sending the action set as `tools` and asking for a command
produced a clean `tool_calls` response — `finish_reason: "tool_calls"`,
well-formed `function.name`/`function.arguments`, no prose, no fence, no
schema violation.

### Implemented, but shipped opt-in, not as the default

`agent.Client.NativeTools` (CLI: `-native-tools`) sends the five actions
as OpenAI `tools` and normalizes whatever comes back — `tool_calls`, or a
recognized raw shape like xLAM's array below — into the exact JSON text
`ParseAction` already expects, so nothing downstream needed to change.
History is reshaped into proper multi-turn tool-calling form at the wire
layer only (`buildToolMessages`): the runner still stores plain
user/assistant text (`Action.ReplayJSON`), and each request reconstructs
`{assistant: tool_calls}` + `{tool: result}` pairs from it. This was not
optional — sending a model's own past turn back as flattened plain
content (the obvious, simpler thing to do, and originally what shipped)
measurably pulled Qwen2.5-1.5B back OUT of tool-calling on the very next
turn, degrading to the same broken shape below.

**Two more things had to be true before that first success generalized,
and one of them is why it's still opt-in:**

1. **The prose system prompt has to be replaced, not supplemented, when
   using tools.** Reproduced with certainty: sending the *full* system
   prompt (which dictates "reply with EXACTLY one JSON object matching
   this schema" and shows the literal shape) *together with* `tools`
   broke Qwen2.5-1.5B — instead of `tool_calls`, it produced a degraded
   hybrid, a bare `{"name":...,"arguments":{...}}` object as plain
   content. The tool definitions already carry the schema; a second,
   conflicting instruction source confuses the model rather than
   reinforcing it. Fixed with a separate, minimal `toolSystemPrompt` —
   rules only (turn discipline, hint semantics, pass/fail judgement),
   no embedded JSON shape — used only when tools are in play.

2. **Even with both of the above fixed, the same model degrades on its
   *second* tool call in one conversation.** Reproduced in complete
   isolation, no `slmtest` involved — a minimal, correctly-shaped
   request with one prior `{assistant: tool_calls}` / `{tool: result}`
   pair, asking for `finish_step` next, got back prose instead of a
   tool call: sometimes fake call syntax (`finish_step(step_result="pass",
   reason="...")`), sometimes just narration with no call attempt at
   all. Confirmed on a *freshly restarted* server (ruling out
   session/process drift — see "A note on long-lived servers" below) —
   `echo-test.md`, which reliably passes in 2 clean turns on the prose
   path, reliably fails in 4 with `-native-tools` on the same fresh
   server, 3/3 runs each way. This is the reason `NativeTools` defaults
   to **off**: it produces a real, reproducible regression on a model
   that otherwise works well on the prose path — a different problem
   from xLAM's (see "Correction" below), where the issue was never
   native tool-calling itself but reaching it at all. A capability that
   can make a working setup worse doesn't get to be the default until
   it's shown to be a net win on more than one model — compare both
   modes against your own before trusting either.

**Against xLAM, the same request produced a hard 500**:
`"The model produced output that does not match the expected peg-native
format"`. Traced to the cause, not just the symptom, because it matters
for how the client-side fallback has to work:

- `llama-server`'s native tool-call parsing is a **fixed, closed list**
  auto-selected from the model's chat template, with no flag to pick a
  different parser: Llama 3.1/3.2/3.3, Functionary v3.1/v3.2, Hermes 2/3,
  Qwen 2.5, Qwen 2.5 Coder, Mistral Nemo, Firefunction v2, Command R7B,
  DeepSeek R1. xLAM isn't on it.
- xLAM is Qwen2-architecture and inherits a Qwen-family template
  signature, so the auto-detector reasonably matches it to the **Qwen
  2.5 parser** — which expects Qwen's `<tool_call>{...}</tool_call>`-tagged
  format. xLAM was fine-tuned to emit a bare `[{"name":...,
  "arguments":{...}}]` array instead — confirmed directly from
  [Salesforce's own model card](https://huggingface.co/Salesforce/xLAM-2-1b-fc-r-gguf),
  which documents that exact array shape as the model's native format,
  and explicitly notes *"users may need custom post-processing to
  convert the JSON output into standard OpenAI format"* for
  `llama.cpp`. Detected parser and actual model output diverge, and the
  server throws rather than degrading to a generic fallback.
- The only relevant server-side flag, `--skip-chat-parsing` ("force a
  pure content parser... model will output everything in the content
  section"), doesn't fix this — it's a blunt, server-wide, all-models
  switch that still leaves the raw array sitting in `content` for the
  client to parse. Setting `tool_choice: "none"` per-request has the same
  effect and was used to confirm the model's raw output was clean: with
  `tools` still present (so the template still shapes the prompt) but
  parsing disabled, xLAM returned exactly
  `[{"name": "run_command", "arguments": {"command": "echo
  hello-from-pty", "wait_ms": 0}}]` — well-formed, no comma bug, no
  stray fields. The model was never the problem; `llama-server`'s
  normalization layer for this specific template mismatch was.

**Conclusion: there is no server-side parameter that fixes this** — it
has to be handled client-side. `Complete` tries three tiers, each a
fallback for the one before, remembering per-`Client` which tiers are
known-broken so a deterministic failure doesn't pay its retry ladder
every subsequent turn:

1. **`toolsCalling`** — `tools` in the request, expect `tool_calls` back.
   The clean win above, for any model/server pair inside the closed list.
2. **`toolsPromptOnly`** — if that hard-fails, retry with `tools` still
   present (so the model's own template still shapes the prompt) but
   `tool_choice: "none"`, so the server never attempts the parse that was
   failing. The reply lands in plain `content`, in the model's own
   native shape, which `normalizeContent` then has a chance to recognize
   — currently xLAM's flat array.
3. **`toolsOff`** — the original prose schema, if even that fails.

This also gives a concrete, sourced list of model families that get
clean `tool_calls` for free at tier 1 with zero extra code (see above) —
worth checking a new model against before assuming it needs tier 2 or 3.

### Correction: the comma-drop bug was never xLAM's — it was ours

Everything logged above under "Against xLAM" — the comma-drop defect,
the non-self-correcting retry loops, "no improvement over Qwen2.5-1.5B"
— was measured with `-native-tools` either off, or (before tier 2
existed) falling all the way back to `toolsOff` on every single turn.
**In neither case did xLAM's traffic ever actually go through its own
designed template.** The prose schema is exactly where the comma-drop
defect lives — it was never a defect *in xLAM*, it was a defect in
asking xLAM to talk a language it wasn't trained on, which is true of
every model tested this way, not something specific to xLAM.

With tier 2 (`toolsPromptOnly`) implemented, xLAM was re-run on the same
specs, genuinely in its own native format this time:

- **`echo-test.md`: pass, 2 clean turns, zero errors** — better than
  *both* the earlier prose-mode result (3 turns, one schema violation)
  and the pre-tier-2 `-native-tools` attempt (hard 500).
- **`tui-editor-test.md`: 3/6 steps pass, including the ground-truth
  `cat`-verification step genuinely confirming written content — and
  the comma-drop defect is completely gone.** All 8 turns of the
  longest-running step parsed as valid JSON on the first try, zero
  parse errors, across the whole run. What remains is a real behavioral
  limit, not a formatting one: the model loses track of whether it's
  still inside vi's insert mode or back at a plain shell, alternating
  `send_keys` and `run_command` without ever resolving it. That is a
  genuine capability ceiling — no schema or prompt fix reaches it — but
  it is a *different, much smaller* problem than "produces malformed
  JSON," which is what every earlier xLAM run had actually been
  measuring.

The remaining two specs were then run the same corrected way, completing
the picture across all four:

- **`workspace-test.md`: 4/5 pass**, up from 3/5 measured in the broken
  mode — steps 1–3 and 5 all genuinely verified, step 4 lost to
  turn-exhaustion. No parse errors anywhere in the run.
- **`tui-claude-test.md`: 2/6 pass**, and notably *not* an improvement —
  the first four steps, including the trivially simple "create a
  folder," were lost to the model looping without ever resolving to
  `finish_step`.

**Correction: "zero parse errors" did not hold here, and this needed a
deeper look rather than trusting an earlier spot check.** Counted
precisely across all four corrected-mode runs: 94 total turns, 9 errors,
every single one inside `tui-claude-test.md` (9 of its 38 turns) — the
other three specs are genuinely 0 of 56. The comma-drop defect
specifically never reappeared anywhere, in any run — but three *new*
malformed shapes showed up under this spec's "read the screen without
acting" step, none seen before:

- **Plain prose, no JSON at all**: `"The screen is already showing, so
  no additional waiting is needed...."` — not malformed JSON, no attempt
  at JSON whatsoever.
- **A novel pseudo-schema, repeated byte-for-byte identically 6 times in
  a row**: `` ```json\n{"type": "function", "function": "wait",
  "parameters": {"wait_ms": 1500}}\n``` `` — structurally, this looks
  like a JSON-*Schema description of a tool* (the `type`/`function`/
  `parameters` shape used to *define* a function) rather than a *call* to
  one, and the same non-self-correcting-loop pattern already documented
  for the 0.5B and for xLAM pre-fix reappears here in a new shape.
- **A hallucinated action** in the cleanup step: `{"action":"cd",
  "folder":"/tmp"}` — `cd` isn't one of the five defined actions, and
  `folder` isn't one of its parameter names; both invented.

**A second, more consequential correction: the step 1 failure is not
what it looked like at first glance, and it is the most important finding
in this comparison.** Both models were run against the *identical*
first command (`mkdir -p ... && cd ... && pwd`), and both got back
byte-for-byte identical PTY output — confirmed by diffing the two full,
untruncated transcripts:

```
bash-3.2$ mkdir -p /tmp/slmtest-claude-tui && cd /tmp/slmtest-claude-tui && pwd
/tmp/slmtest-claude-tui
bash-3.2$
```

The large model read this, said so explicitly (`"thought":"The pwd
output confirms the directory exists and is the current working
directory."`), and called `finish_step` on turn 2. **xLAM was shown the
exact same unambiguous, complete success confirmation and did not
recognize it** — its turn 2 reply carried no `thought` field at all and
issued a redundant `send_keys "cd /tmp/slmtest-claude-tui"`, repeating
work already done. This was never a timing problem, an environment
difference, or a JSON-formatting problem — it is a plain failure to
check the evidence already in front of it against the goal it was given,
using identical input to a model that got it right.

**That single misstep then cascaded into a harness-relevant discovery.**
Two turns later, xLAM sent `send_keys "pwd"` with `press_enter` unset
(false) — typing `pwd` without executing it. The harness's
`notExecutedNote` correctly told it so: *"that text has been typed into
the terminal but has NOT run... Use run_command to execute it."* xLAM
did exactly that on the next turn — `run_command "pwd"` — but the
**unexecuted `pwd` was still sitting in the terminal's input buffer**,
and the new command concatenated onto it: the shell received literally
`pwdpwd`, produced `bash: pwdpwd: command not found`, and xLAM — now
facing a confusing error it had no way to diagnose as self-inflicted —
abandoned the approach and started over, repeating the same mistake a
second time a few turns later with the same result. **`notExecutedNote`
describes what already happened but not what it leaves behind**: it
doesn't warn that the stranded keystrokes will corrupt whatever is typed
next. That gap is real and is not specific to xLAM; any model that acts
on the note's advice without also clearing the stale input first hits
this. Worth fixing in the harness rather than leaving as a per-model
finding — see Known Gaps.

**One more concrete contrast, in step 4** (leaving the TUI): the large
model sent `send_keys` with `command: ""` — the actual Escape
control character. xLAM sent `send_keys` with `command: "esc"` — the
literal three-letter string, typed into the terminal as three ordinary
characters, which does nothing. Small, but exactly the kind of precise
mechanical slip that compounds into the larger failure once nothing in
the step resolves.

**Revised conclusion.** The comma-drop defect is gone, permanently, and
that finding still stands — 0 recurrences across 94 turns and four
specs. But `tui-claude-test.md` shows xLAM's JSON reliability is not
uniformly solved; new malformed shapes surface under different
conditions (notably a step with nothing to *do*, only something to
*read*). More importantly, this spec's real story isn't formatting at
all: xLAM lost the same task the large model won on identical evidence,
through a plain failure to verify its own goal against output already in
front of it, then compounded that with a mechanical slip (`send_keys`
without clearing a stale buffer) that the harness's own guidance doesn't
yet warn against. "Mechanically solid, behaviorally inconsistent" from
the earlier revision undersells it — on this spec specifically, xLAM's
formatting was inconsistent too, in ways distinct from anything logged
before the fix.

### A note on long-lived servers

While chasing the second-tool-call finding, the same `echo-test.md` run
that had passed cleanly for hours suddenly started failing consistently
— the model repeating a byte-for-byte identical malformed reply
(a stray `//` comment inside otherwise-valid JSON) across three separate
`slmtest run` invocations against the same long-lived `llama-server`
process. Restarting that process with nothing else changed restored the
clean 2-turn pass, 3/3 times, with the small wording variation you'd
expect from genuine per-request sampling. A `llama-server` instance that
has absorbed hours of varied traffic (this session ran repeat-penalty
experiments, tool-calling tests, and model comparisons all against the
same port) can drift into a state that reproduces one bad output
deterministically rather than sampling normally — restart it before
concluding a regression is real, especially one that looks suspiciously
*more* consistent than a genuine model failure should be.

## Sampling parameters matter — but there's no universal right answer, and thinking-mode flags don't apply here

Following the `tui-claude-test.md` deep dive, checked whether the harness
was even running xLAM with sensible sampling settings before attributing
more to "capability." It wasn't: Salesforce publishes an exact
`generation_config.json` alongside the weights —
`temperature: 0.7, top_p: 0.8, top_k: 20, repetition_penalty: 1.1,
do_sample: true` — and the harness was sending **`temperature: 0.1`**
(roughly a seventh of the recommendation) with `top_p`/`top_k`/
`repetition_penalty` left at `llama-server`'s own defaults
(`repeat_penalty` defaults to `1.0`, i.e. **disabled**).

Confirmed live, via `llama-server`'s `/slots` endpoint, that a
request-level `temperature` field always overrides the server's CLI
default, but fields the client never sends (`top_p`, `top_k`,
`repetition_penalty`) correctly pick up whatever the server was launched
with. So the client's hardcoded `0.1` was silently overriding the one
setting that mattered most, while the other three were reachable for
free just by launching `llama-server` with the right flags. `Client`
gained a `Temperature` field (CLI: `-temperature`, default unchanged at
`0.1`) so an operator can match a model's own published defaults instead
of fighting a generic control-loop setting.

**Re-ran `tui-claude-test.md`** with the server launched
`--temp 0.7 --top-p 0.8 --top-k 20 --repeat-penalty 1.1` and the client
at `-temperature 0.7` — genuinely closer to what the model's authors
intended than any prior run in this log.

**Result: a real, measurable improvement in overall completion — 4/6
steps pass, up from 2/6 — but neither of the two root causes found in
the deep dive was fixed:**

- **The evidence-verification miss from step 1 still happened.** Shown
  the identical successful `pwd` output as before, turn 2 still didn't
  call `finish_step` — this time manifesting as `send_keys "pass"`,
  literally typing the word into the terminal rather than issuing a
  verdict. Arguably *closer* to correct (the content "pass" is right,
  the mechanism is wrong) but still not a resolution, and still the same
  underlying miss: not recognizing that the goal was already met.
- **Step 3's non-self-correcting loop still happened, in a very similar
  shape.** The model repeated `` ```json\n{"step_result": "pass",
  "reason": "..."}\n``` `` — correct verdict *content*, but missing
  `"action": "finish_step"` — identically, 6 turns straight, with
  repetition penalty explicitly enabled at the model's own recommended
  1.1. Total error count for the run was actually slightly *higher*
  than the untuned run (11 of 35 turns vs. 9 of 38) — the loop just
  moved to omitting a field instead of misnaming the shape entirely.

The `tui-claude-test.md` result above (4/6 vs. 2/6) rests on one run per
setting — too small a sample to trust on its own, especially after what
came next. It was re-checked properly.

### There is no universal "correct" temperature — checked with a fair, same-server, repeated comparison

Ran `echo-test.md` three times at `-temperature 0.1` and three times at
`-temperature 0.7`, same freshly-restarted server each time, both models:

| | xLAM (temp 0.1) | xLAM (temp 0.7) | Qwen2.5-1.5B (temp 0.1) | Qwen2.5-1.5B (temp 0.7) |
|---|---|---|---|---|
| Result | **0/3 pass** | **3/3 pass** | 2/3 pass (consistent with this session's whole history at 0.1) | **0/3 pass** |

**xLAM's improvement is real and reproducible** — 0/3 at its harness
default, 3/3 at its own published setting, confirmed twice now
(`tui-claude-test.md`'s 4/6-vs-2/6 plus this clean 0-vs-3 result on the
simplest spec in the suite). Matching its `generation_config.json`
measurably, repeatably helps.

**Qwen2.5-1.5B moves in the opposite direction on the exact same
setting.** It shares xLAM's identical published defaults — same base
architecture, byte-identical `generation_config.json` values — yet
raising its temperature from this harness's `0.1` default to that same
`0.7` took it from "usually passes" to **0 passes in 6 combined runs**
across both comparison batches, on a spec that has been essentially
bulletproof all session at `0.1`.

**Conclusion, revised from the single-run version above: a model's
published `generation_config.json` is not a reliable signal for what a
narrow, structured, single-JSON-object-per-turn control loop needs — it
was tuned for open-ended chat quality, and this harness is not that
task.** xLAM (fine-tuned specifically for function calling, presumably
trained expecting some sampling diversity to escape a narrow, repetitive
completion) needs its higher setting to stop getting stuck; Qwen2.5-1.5B
(general instruction-tuned, never specifically trained for this exact
narrow JSON-per-turn shape) needs the opposite — low temperature to keep
reliably reproducing the pattern it already does well, where added
sampling diversity just as reliably breaks it. Two models, identical
published defaults, opposite empirically-correct answers. **The
practical guidance: don't apply a model's published defaults on the
assumption they'll help this kind of task — test both extremes
per-model, cheaply, the way this comparison did**, rather than trusting
either the harness's generic default or the model card's chat-tuned one
without checking.

None of this reaches the two root causes from the `tui-claude-test.md`
deep dive, on either model at either setting: the evidence-verification
miss and the incomplete-schema loop persisted through every sampling
configuration tried. Those still read as reasoning/attention limits at
this model size, not decoding-parameter artifacts.

### Thinking-mode parameters: checked, not applicable to either model

`llama-server` has a family of reasoning-related flags
(`--reasoning-format`, `--reasoning-effort`, `--reasoning-budget`,
`--reasoning-preserve`) that control `<think>`-tag handling — but only
for a model whose own chat template defines that behavior. Checked both
models' actual `tokenizer_config.json` chat templates directly (not
assumed): neither Qwen2.5-1.5B-Instruct's nor xLAM-2-1b-fc-r's template
contains any thinking/reasoning conditional at all. These flags are
structural no-ops for both — there's no template branch for them to
control. Relevant only for reasoning-native families (DeepSeek-R1, QwQ,
Qwen3 with thinking enabled, GPT-OSS, and similar) not used in this log.

### One more real finding, tested and set aside: xLAM's own chat template silently drops its format instructions

xLAM's template has an `{%- if messages[0]['role'] == 'system' %}`
branch: when the first message is a system message — which this harness
always sends — the template inserts *only* that message. Its own
built-in `format_instruction` (explaining the array-of-calls shape, and
explicitly permitting "for tasks that don't require tools... respond
directly in plain text") is defined in the `{%- else %}` branch and
**never fires while a caller-supplied system message is present.**
Confirmed structurally by reading the template, then tested live by
comparing identical requests with and without a system message present.

The hypothesis — that restoring `format_instruction` would fix the
read-only-step failures from the deep dive — did not hold up. Reproducing
the exact failing scenario (a step where nothing needs to run, only
`wait`/`finish_step` are offered) three times each way: *with* the
system message, the model consistently called `wait` — wrong, but valid,
executable JSON. *Without* it, letting `format_instruction` fire as the
template intends, the model instead wrote plain, unparseable prose
explaining why `wait` didn't fit the task, three times in a row, never
reaching `finish_step` either way. Dropping the harness's own system
message would trade one failure mode for a different, less recoverable
one — not fix anything — so this was tested and deliberately not
adopted, rather than left untried.

## Qwen2.5-Coder-7B-Instruct: closing the gap between xLAM and DeepSeek-V4-Flash

Looking for something between xLAM-1B (promising but not good enough —
see above) and a full-size hosted model like DeepSeek-V4-Flash, that
still fits an M1 Mac with 32GB RAM: `Qwen/Qwen2.5-Coder-7B-Instruct-GGUF`
at `Q4_K_M` (~4.7GB), served via
`llama-server -hf Qwen/Qwen2.5-Coder-7B-Instruct-GGUF:Q4_K_M --port 8083
-c 8192`. Chosen deliberately as the *safe* option first — a
proven-compatible architecture already on `llama-server`'s native
tool-call-parser list — before attempting the riskier, higher-ceiling
Qwen3.5-9B (novel hybrid architecture, real compatibility risk with the
installed `llama.cpp` build).

**`echo-test.md` (baseline sanity check), prose mode, 3 runs each:**
6/6 clean 2-turn passes at both temp 0.1 (harness default) and temp 0.7
(this model's published default). No surprises here — this is the
easy case every model in this log has cleared.

**Prose mode, harder specs, single run each:**

| Spec | Result |
|---|---|
| `workspace-test.md` | 4/5 steps pass (step 4 fails — same sandbox-write finding every model in this log reports; not a Coder-7B-specific issue) |
| `tui-editor-test.md` | fails at step 1, then step 2, aborts at step 4 |
| `tui-claude-test.md` | passes step 1, fails step 2, aborts at step 4 |

**`-native-tools` mode: a 100% harness bug, not a model failure.**
The first `-native-tools` run of the three harder specs showed a
suspicious 100% turn-error rate — 30/30 (`workspace-test.md`), 48/48
(`tui-editor-test.md`), 48/48 (`tui-claude-test.md`), 126/126 total —
every single turn erroring with `unknown action type ; must be one of:
run_command, send_keys, wait, finish_step, abort_test`. A 100% *parsing*
failure with a suspiciously uniform shape is itself a signal to check the
harness before blaming the model, per this log's usual method. Direct
inspection of raw replies across all three specs confirmed a consistent,
reproducible pattern:

```
{{"name": "run_command", "arguments": {"command": "vi /tmp/slmtest-tui.txt"}}
```
```
```json
{"name": "run_command", "arguments": {"command": "mkdir -p /tmp/slmtest-claude-tui && cd /tmp/slmtest-claude-tui && pwd"}}
```
```

Qwen2.5-Coder-7B was deterministically producing a well-formed native
tool-call JSON object — just a **bare single object**
(`{"name": ..., "arguments": {...}}`), sometimes wrapped in a
` ```json ` fence — rather than xLAM's **array-wrapped** shape
(`[{"name": ..., "arguments": {...}}]`). `normalizeContent()` in
`internal/agent/tools.go` only recognized the array shape and never
stripped code fences first, so every reply in this shape was passed
through to `ParseAction` unmodified, which correctly reported it as
missing an `"action"` field.

**Fix:** `normalizeContent()` now strips a markdown code fence before
inspecting the content, and recognizes a bare single-object shape in
addition to the array shape — guarded so it never misfires on the
harness's own canonical `{"action": ...}` schema (checked by requiring
`"name"` present and `"action"` absent before treating content as a
native tool call). Covered by
`TestNativeBareObjectContentIsRecognized`,
`TestNativeBareObjectFencedContentIsRecognized`, and
`TestOwnSchemaContentIsUntouchedByNormalize` in
`internal/agent/schema_test.go`.

**Re-run after the fix, same three specs, `-native-tools` mode:**

| Spec | Prose mode | Native mode (fixed) |
|---|---|---|
| `workspace-test.md` | 4/5 steps pass | 4/5 steps pass (12 turns, 0 harness errors) |
| `tui-editor-test.md` | fails step 1 | **passes steps 1–2**, step 3 fails on turn budget (18 turns, 0 harness errors) |
| `tui-claude-test.md` | fails step 2 | **passes steps 1–2**, step 3 fails on turn budget (13 turns, 0 harness errors) |

Zero `unknown action type` or "not valid JSON" harness errors traceable
to the old bug across all 43 turns — the fix is confirmed to work, not
just plausible. Native mode also did genuinely better than prose mode on
both TUI specs, clearing steps that prose mode failed outright.

**A new, distinct model-behavior failure mode, unmasked by the fix.**
With the harness bug gone, a different and consistent pattern showed up
in both TUI specs plus two isolated turns in `workspace-test.md`: after
one successful `send_keys`/`wait` turn, the model's *next* reply is
sometimes not a new action at all but a verbatim echo of the harness's
own previous user-turn framing, wrapped in a `<tool_response>` tag it
was never asked to produce, e.g.:

```
<tool_response>
Terminal output:
(none)

NOTE: you have now run that exact command 2 times in a row and the terminal output above has not changed...
```

This is almost certainly Qwen2.5-Coder's own Hermes-style tool-use chat
template convention (tool outputs are normally wrapped in
`<tool_response>` on the *input* side) bleeding into its *output* — the
model appears to occasionally mistake "you are shown a tool response"
for "you should produce a tool response", especially once the harness's
own turn-limit warning text is present in what it's echoing. In
`workspace-test.md` steps 1–2 it self-corrected after one bad turn (the
harness's "could not be parsed, reply again" nudge was enough); in both
TUI specs' step 3 it got stuck repeating the echo verbatim turn after
turn until the budget ran out, never reaching `finish_step`. This is a
genuine model capability limit specific to `-native-tools` mode, not a
harness bug — there is no tool call embedded in an echoed observation for
`normalizeContent` to extract, so nothing to fix here; it's recorded as a
finding about the model, and a reason `-native-tools` mode on this model
still needs prose-mode's turn-budget safety net rather than being
strictly superior end to end.

**Overall assessment:** genuinely a step up from xLAM-1B — it clears TUI
steps xLAM and the smaller Qwen sizes never reached — but the harder
specs' step 3+ still expose real limits (sandbox-write awareness,
turn-budget exhaustion once stuck echoing). A real candidate "between
xLAM and DeepSeek-V4-Flash" as asked, though not a model that clears
every step in this log's hardest specs unattended.

## Qwen3.5-9B: the strongest model in this log so far

The second half of the "something between xLAM and DeepSeek-V4-Flash"
search: `unsloth/Qwen3.5-9B-GGUF` at `Q4_K_M` (~6GB), served via
`llama-server -hf unsloth/Qwen3.5-9B-GGUF:Q4_K_M --port 8084 -c 8192`.
Picked as the higher-ceiling, higher-risk option — Qwen3.5 uses a novel
hybrid Gated DeltaNet + MoE architecture, and the base repo
(`Qwen/Qwen3.5-9B-GGUF`) doesn't actually exist; the real GGUF quants
live in community repos (`unsloth/...`, `bartowski/Qwen_Qwen3.5-9B-GGUF`,
`lmstudio-community/...`) — worth knowing before assuming an
official-looking repo path is correct.

**Compatibility check first, since this is a novel architecture:**
loaded cleanly on the installed `llama-server` (`0.3.0`, build `10621`,
already current — see the stale-engine section below), including its
multimodal projector. No crash, no "unknown architecture" error. The
`echo-test.md` baseline passed cleanly in 2 turns. Compatibility risk did
not materialize.

**Thinking mode is genuinely active for this model** — unlike every
other model in this log, where `--reasoning-format`/`--reasoning-effort`
were confirmed to be no-ops because the chat template didn't define
`<think>` handling. Qwen3.5 operates in thinking mode by default per its
model card, and a raw API probe confirmed `llama-server` automatically
splits the `<think>...</think>` block into a separate
`reasoning_content` field, leaving `content` clean — no flag needed, no
harness change needed. A representative turn: 216 completion tokens,
~10.5s, most of it spent in `reasoning_content` before a one-line
`content` answer. This is a real, working instance of the machinery the
other models' testing had only shown as inert.

**Published sampling settings**: no `generation_config.json` exists for
this model at all (unlike prior models in this log). The model card's
README recommends, depending on mode: thinking/general
`temperature=1.0, top_p=0.95, top_k=20, presence_penalty=1.5`;
thinking/precise-coding `temperature=0.6, ...`; instruct/non-thinking
`temperature=0.7, top_p=0.8, ...`. Tested at the harness default (0.1)
and at 0.6 (closest match to this harness's "precise, single-JSON-object
per turn" task shape).

**Prose mode results:**

| Spec | temp=0.1 | temp=0.6 |
|---|---|---|
| `workspace-test.md` | **5/5 pass** | 5/5 pass |
| `tui-editor-test.md` | **6/6 pass** | fails at step 5 |
| `tui-claude-test.md` | fails at step 3 | fails at step 3 |

`tui-editor-test.md` at temp=0.1 is a first for this log: no other model
has cleared every step of that spec. Repeated twice more (3/3 total) to
rule out a lucky single run — all three runs passed all 6 steps cleanly,
2–4 turns per step. `workspace-test.md` step 4 (the sandbox-write check)
is also correctly judged here in a way earlier models got wrong: the
spec's `Expect` text explicitly says pass either way and state which
case occurred (sandboxed-refused vs. unsandboxed-succeeded) — Qwen3.5-9B
read that nuance correctly ("`exit=0` indicates the shell allowed writing
outside the workspace (no sandbox active). Step passes as expected per
the goal."), whereas prior models in this log treated the unsandboxed
case as an automatic fail, misreading the spec.

`tui-claude-test.md` step 3 ("read the menu the TUI is offering") is the
one step this model doesn't clear at either temperature, but it fails it
*honestly* — reasoning quoted directly: "the expected menu with two
numbered options... is not clearly visible in the rendered output,"
which matches this log's own repeated observation that this specific
step is a genuinely hard TUI-rendering-timing case, not a model
reasoning failure. It reports what it actually sees rather than
hallucinating a pass.

temp=0.1 outperformed temp=0.6 here, consistent with (though not
predicted by) the "no universal right temperature" finding above.

**Native-tools mode**, temp=0.1: `workspace-test.md` 5/5 pass (14 turns,
0 harness errors). `tui-claude-test.md` fails the same step 3 as prose
mode, for the same honest reason. `tui-editor-test.md` regresses to a
step-3 failure it never showed in prose mode (3/3), and the raw
transcript pins down why: its first `send_keys` reply was
`{"action":"send_keys","command":"\\u001b",...}` — a **double-escaped**
JSON string, decoding to the six literal characters backslash-u-0-0-1-b
rather than the single Escape byte prose mode reliably sent. Vi never
actually left insert mode, and every subsequent `:q`/`:q!`/`run_command`
attempt landed as literal inserted text instead. This is a genuine, well-formed-JSON
model-behavior difference between how this model fills a field in
native-tools mode vs. free-form prose generation, not a parsing bug —
0 harness errors across all 39 native-mode turns.

### The one failing step was a poisoned test fixture, not a model limit

Going deeper on the single remaining failure (`tui-claude-test.md` step
3) turned up something more important than a model-capability gap: the
step was structurally incapable of passing, for every model tested in
this log, because of a stale test fixture — not because of anything any
model did wrong.

`tui-claude-test.md` used a fixed directory,
`/tmp/slmtest-claude-tui`, across every step of every run. Claude Code
records trust decisions per path in `~/.claude.json`, independent of
whether the directory still exists on disk — step 6's `rm -rf` deletes
the directory but not that record. At some point earlier in this
session's long testing history, a run against that exact path ended up
with `"hasTrustDialogAccepted": true` recorded for it (most likely an
earlier model selecting "trust" instead of declining). From that point
on, every subsequent run of this spec — against every model, including
all three Qwen3.5-9B runs reported above — launched `claude` into an
*already-trusted* folder. It never saw a trust prompt at all; it saw the
ordinary "Welcome back Scott!" / ready-to-type screen instead. Confirmed
directly from a raw transcript: step 2's captured PTY output for the
"failing" run contains that welcome text and a live `❯` prompt, no trust
question anywhere in it. Every model's step-3 "menu not visible" verdict
was an honest, correct read of what was actually on screen — the harness
had quietly stopped testing what the spec claimed to test.

**Fix**: changed step 1 to `export TUIDIR=$(mktemp -d
/tmp/slmtest-claude-tui-XXXXXX) && cd "$TUIDIR" && pwd` instead of a
fixed `mkdir`, relying on the PTY's per-run persistent shell state (env
vars and cwd already carry across steps within one run — see "Steps run
in order" in `CLAUDE.md`) so every later step's `$TUIDIR` reference
resolves to that run's unique, guaranteed-never-trusted path. Step 6's
cleanup was updated to match. This needed no change to
`~/.claude.json` at all — a fresh name every run makes the stale trust
record permanently irrelevant rather than fixing it once.

**Re-run against Qwen3.5-9B with the corrected spec, temp=0.1, 3 runs:**
3/3 full 6-step clean passes. Step 3's reasoning on each run explicitly
references seeing a real two-option trust menu (e.g. "displaying the
trust prompt menu with two numbered options (trust/decline)"), and step
4 correctly declined via Escape each time, confirmed by checking
`~/.claude.json` afterward: no new trust entry was created for the
run's `$TUIDIR` path, only the original stale entry for the old fixed
path remains (harmless now that nothing references it). Also repeated
`workspace-test.md` two more times (3/3 total) for the same confidence
level as the other two specs.

**Corrected overall assessment: Qwen3.5-9B at temp=0.1 clears all three
of this log's hardest specs, 3/3 clean runs each** —
`workspace-test.md`, `tui-editor-test.md`, and, once the fixture bug was
fixed, `tui-claude-test.md` too. This is the first model in this entire
log to fully clear every hard spec tested, and a real, well-verified
answer to "something between xLAM and DeepSeek-V4-Flash." The
native-tools-mode caveat above still stands (the double-escaping
regression on `tui-editor-test.md`), so prose mode remains the
better-supported choice for this model today — but in prose mode, this
result is about as clean as this log gets.

**A broader lesson, independent of this specific model**: a spec that
reuses a fixed path across runs, against a tool that remembers state by
path outside the directory itself, can silently stop testing what it
claims to test — and the failure it produces (a plausible-sounding
"menu wasn't visible yet") is exactly the kind of failure that doesn't
announce itself as an environment bug. Every prior model in this log
that "failed" this step was actually correctly reporting on a poisoned
fixture, not exhibiting a capability limit — worth remembering before
attributing a suspiciously consistent single-step failure across many
different models to "this step is just hard."

### Rechecking the other models against the fixed spec

Given the fixture bug above affected every model that ran
`tui-claude-test.md`, it was worth checking whether xLAM's and
Qwen2.5-Coder-7B's earlier results on this spec were actually confounded
by it too, rather than assuming only Qwen3.5-9B was affected.

**xLAM-2-1b-fc-r**, re-run against the fixed spec at temp=0.7 (its own
established best setting): fails at **step 1** — before Claude Code is
even launched — with its own well-documented defect, reproduced again:
`[{"thought": "..."}, {"action": "run_command", ...}]`, a two-element
JSON array instead of one merged object, repeated identically 8/8 times
with no self-correction. This is the same non-self-correcting
malformed-JSON class already documented above for this model, not
something the fixture bug caused or hid — xLAM never got far enough to
reach the poisoned step 3 either before or after the fix.

**Qwen2.5-Coder-7B-Instruct**, re-run against the fixed spec at temp=0.1,
both prose and `-native-tools` mode: fails at **step 2** both times, in
the same way it failed against the *original* spec — given the bare
Hint `claude`, it replies `wait` twice instead of ever issuing
`run_command: claude`, then fails, claiming the command "did not run."
Confirmed by direct comparison: the old-spec and new-spec runs produce
the same `wait`-instead-of-`run_command` action on turn 1 of step 2, in
both tool modes. This is a genuine, reproducible model quirk — treating
a bare command-name Hint as something already running rather than
something to type — independent of the fixture bug. Coder-7B never
reliably reaches step 2 successfully across runs, so it never reaches
step 3 either.

One genuine confound is worth flagging rather than glossing over: an
earlier native-tools run of the *unfixed* spec (documented above) *did*
pass steps 1–2 and reach step 3, failing there via the `<tool_response>`
echo-loop. That specific result was produced while the fixture was
already poisoned, so it cannot be cleanly separated into "the echo-loop
would have happened regardless" versus "seeing an already-trusted screen
contributed to the confusion." The fresh recheck above shows step 2
itself is flaky for this model (sometimes it launches `claude`
correctly, sometimes it doesn't), which is itself the more useful
finding — it means this model's `tui-claude-test.md` result was never
reliable enough to isolate a single root cause at step 3, fixture bug or
not.

**Net effect of the fixture bug on the model comparisons in this log**:
none, for xLAM and Coder-7B — both have their own independent, earlier
failure modes that stop them well short of the affected step. It was
consequential for exactly one model, Qwen3.5-9B, which was the only one
in this log ever good enough to reach a genuinely clean step 3.

### Why the large model's original run wasn't affected

The large model (`DeepSeek-V4-Flash`, referred to as "the large model" /
"LLM" throughout this log) ran `tui-claude-test.md` first, at the very
start of this spec's testing history — see "Against Claude Code's own
TUI (large model only...)" above. That run explicitly checked
`~/.claude.json` and the filesystem afterward and confirmed **no project
entry existed** for the test path at all. That is direct evidence the
fixture was still clean at that point: the poisoning happened later,
from some run after this one — this log does not pin down exactly which
one, and given how many runs against this same fixed path preceded the
discovery, reconstructing the exact culprit isn't possible after the
fact. The large model's clean 6/6 result stands as genuinely verified,
not a beneficiary of the bug — it simply ran before the fixture was ever
poisoned, on a path that was still legitimately new to Claude Code.

## Going further with Qwen3.5-9B: a real conversation through the TUI

With the trust fixture fixed and Qwen3.5-9B clearing every hard spec,
the natural next step was to push past "trust and decline" into an
actual conversation: trust the folder, send one real message, read a
real reply, and exit cleanly. This turned up two genuine harness bugs —
neither specific to this model — that a "decline and leave" spec was
never going to exercise.

### Bug 1: Enter was sending the wrong byte for a raw-mode TUI

The new spec's step 3 asks the model to confirm the trust menu's
already-highlighted default option by pressing Enter alone. Every
attempt failed. Direct inspection showed both `run_command` and
`send_keys(press_enter: true)` funnel through one code path in
`internal/ptydriver/ptydriver.go` that appends `"\n"` (LF) for "Enter" —
never `"\r"` (CR).

Verified empirically, independent of the harness, by scripting a raw PTY
against `claude` directly: sending `\n` alone left the trust menu
unresponsive; sending `\r` alone correctly confirmed the highlighted
option and advanced to the normal input screen. This makes sense in
retrospect — a real Enter key always sends CR, and a canonical-mode
shell's line discipline is lenient enough to accept either, which is
exactly why "\n" had looked fine for every plain shell command tested
in this log so far. A raw-mode TUI (Ink and similar — what Claude Code's
interface is built with) disables that line-discipline translation and
listens for the literal byte a real terminal produces, so it never saw
anything meaningful in a bare `\n`. This spec is the first one in this
log to ever need "confirm a raw-mode menu's default via Enter," which is
why the bug went unnoticed through the rest of this log's testing.

**Fix**: `RunCommand` now appends `"\r"` instead of `"\n"`. This is a
strict correction, not a tradeoff — it matches what a real keyboard
actually sends, and canonical-mode shells handle it identically to LF.

### Bug 2: the schema couldn't express "press Enter alone" at all

Before the CR fix even mattered, the model's very first attempt at this
step — `send_keys` with `command: ""`, `press_enter: true` — was
rejected outright: the schema required a non-empty `command` for both
`run_command` and `send_keys` unconditionally, with no way to say "type
nothing, just press Enter." That is a real, legitimate action (confirm
a highlighted default, submit whatever is already sitting in a TUI's
input line) that the contract simply couldn't represent, forcing the
model into workarounds (typing a redundant "1") that didn't reliably
help.

**Fix**: `internal/agent/schema.go`'s `Validate()` now allows an empty
`command` for `run_command` unconditionally (it always presses Enter, so
this can never be a no-op), and for `send_keys` when `press_enter: true`
is explicitly set (otherwise it would be a genuine no-op — send nothing,
press nothing — which is still correctly rejected). The system prompt
and schema docs were updated to tell the model this is available, rather
than leaving it to rediscover by trial and error. Covered by
`TestParseActionEmptyCommandPressesEnterAlone` in
`internal/agent/schema_test.go`.

### Bug 3 (found, not fixed): a raw double-keypress exit is a losing race against inference latency

Step 5 originally asked the model to exit via two Ctrl-C presses in
quick succession — a common "press again to confirm" TUI pattern.
Every attempt failed the same way: the model sent Ctrl-C, saw "Press
Ctrl-C again to exit," sent Ctrl-C again — and saw the identical prompt,
never the confirmed exit.

Verified this is a genuine timing race, not a model mistake, by
scripting the same sequence directly against a raw PTY: two Ctrl-C bytes
in a single `Write()` call did nothing (they appear to get coalesced
into one read on the application side), but two separate writes with a
~300ms gap between them worked cleanly. The harness's minimum gap
between two *separate* model actions is bounded below by a full model
inference round-trip — for this model in thinking mode, commonly 5-10+
seconds — which is far longer than whatever confirmation window Claude
Code's TUI uses. This makes a double-keypress exit structurally
unreliable through this harness for any model whose inference isn't
fast enough to beat that window, independent of how well it reasons
about the task.

**Not fixed at the harness level** — there is no clean way for a single
JSON action to express "send byte, wait 300ms, send byte again" without
a larger schema change. **Worked around at the spec level instead**:
Claude Code accepts a typed `/exit` command like any normal input,
submitted with a single Enter — verified working cleanly, avoiding the
whole timing problem. `examples/tui-claude-chat-test.md`'s step 5 uses
this instead of Ctrl-C.

### Bug 4 (found, not fixed): the consuming-diff design loses on-screen content the model never acted on

The most consequential finding, and the same root cause as the trust
fixture's masking effect on step 3 earlier in this log, showing up again
in a new place. Step 4 asks the model to send a message and read
Claude's reply. In one run, the reply — literally `⏺banana`, exactly
what was asked for — appeared in the *first* `wait` turn's output,
interleaved with spinner-animation noise. The model didn't act on it
that turn. By the next turn, `SinceLastSnapshot()` had already returned
and discarded that content; nothing new had been written since, so the
model saw empty output and kept waiting — for 140 seconds across the
rest of its turn budget — before failing on turn exhaustion, having
never seen the reply again.

This is the same mechanism as the trust-poisoning investigation's
underlying architecture, not a new bug: `SinceLastSnapshot()` in
`internal/ptydriver/ptydriver.go` returns bytes written since the last
call *and resets the buffer*, giving each turn a "what's new" view with
no persistent notion of "what's currently on screen." For an ordinary
scrolling shell this is the right model — each command's own output is
exactly the new content worth showing, and a command that legitimately
produces nothing should show nothing. But for a raw-mode TUI where
meaningful content sits on screen indefinitely without re-emitting
bytes, the model gets exactly one chance to notice it, in whatever turn
it happens to arrive, however cluttered with unrelated animation noise
that turn's output is.

**Not fixed** — the correct fix is a real terminal-screen model (tracking
cursor position and a persistent grid of visible cells via ANSI/VT
interpretation) rather than a raw byte diff, which is a substantial
rewrite of `ptydriver`'s core model, not a patch. A naive "fall back to
last non-empty output when nothing new arrived" was considered and
rejected: it would fix the TUI case but actively mislead the model in
the far more common case of an ordinary command that legitimately
produces no output (e.g. `mkdir` succeeding silently) by re-showing
stale content as if it were a fresh result. **Worked around at the spec
level instead**: step 4's `Expect` now tells the model explicitly that
this terminal only shows what changed since the last action, that a
reply can arrive alongside spinner noise, and to decide from the turn
where it first appears rather than waiting for it to reappear. Combined
with the `run_command`-not-`send_keys` fix below, this raised the clean
pass rate on this step from failing in both of two earlier attempts to
passing in all three re-runs after the wording change — a real
improvement, but a workaround for one spec's wording, not a fix for the
underlying gap. Recorded as a "Known gap" in `CLAUDE.md` — see there for
the fix direction, so anyone picking it up starts from a full accounting
rather than rediscovering the mechanism.

### A smaller model quirk, also found and worked around

One repeat run showed the model embedding a literal `\n` inside a
`send_keys` command's own text *and* separately setting
`press_enter: true` — sending the message text, an internal newline,
and then the harness's own appended Enter, back to back. The resulting
byte sequence was accepted as valid input by Claude Code's multi-line
capable text box but produced no reply within the observed window
(`"(none)"` for 40+ seconds). Switching the spec's hint to `run_command`
(which has no `press_enter` field to redundantly set, and no reason to
add a trailing newline) removed the option to make this specific
mistake rather than relying on the model not making it — same general
principle as the `press_enter:false` on `run_command` fix earlier in
this log: narrow the schema so a plausible small-model mistake has
nowhere to go, rather than hoping it won't happen.

### Result after all of the above

`examples/tui-claude-chat-test.md` (new spec, not a modification of
`tui-claude-test.md` — that one's decline-only, zero-cost, deterministic
design is worth keeping as-is): 3/3 clean 7-step passes against
Qwen3.5-9B at temp=0.1 after both harness fixes and both spec-wording
workarounds were in place, including a real trust decision, a real
message to Claude, and a real reply read back correctly. Two earlier
attempts with the original step 4/5 wording failed — one on the
Ctrl-C timing race, one on the consuming-diff issue — both explained
above rather than left as unexplained flakiness.

## tui-claude-advanced-test.md: a real plan, real tracked tasks, verified on disk

The natural next step past "trust and have one exchange": give Claude a
real multi-part coding task, let it plan the work into tracked tasks,
wait for it to actually finish, and verify the *result* against the
filesystem rather than trusting anything the TUI claimed. The task
picked deliberately small and cheap to verify: create `greet.py` (a
`greet(name)` function plus a `__main__` block), create `test_greet.py`
(a test asserting its output), run the test, confirm it passes.
`examples/tui-claude-advanced-test.md` is 11 steps: setup, launch, trust,
submit the task, confirm a tracked task list appeared, wait for real
completion, exit via `/exit`, then three separate ground-truth checks
(`cat` the file, actually run it, actually run the test) plus cleanup —
matching this project's own stated principle of ending on ground truth
rather than a screen read.

Getting a real multi-file agentic session working end-to-end surfaced
two more harness bugs and two more findings, on top of everything found
building the simpler chat spec.

### Bug: per-step history has no bound, and real TUI output is dense enough to hit it fast

The first attempt aborted at step 5 with `request (30035 tokens) exceeds
the available context size (8192 tokens)`. Raising `-c` to 32768 pushed
the failure later but did not fix it — it recurred at step 6, then again
at step 5 on a different run at `request (33443 tokens) exceeds ... 32768`.
Root cause, confirmed by reading the code rather than guessing: `msgs`
in `internal/runner/runner.go` accumulates every turn's full raw PTY
output for a step and is never trimmed. Every other spec in this log
finishes a step in 2-8 turns of plain shell text, so this was never
visible before — but a real full-screen TUI redraw is dense with ANSI
escape codes, and step 6 ("wait for Claude to finish") can legitimately
need many `wait` turns for genuinely slow real work.

**Fixed two ways**, because the overflow turned out to have two
independent causes:

1. **Growth across turns**: `trimStepHistory` in `runner.go` now caps a
   step's history to the most recent `maxStepHistoryTurns` (6)
   user/assistant pairs, dropping older ones from the front with a note
   folded into the oldest retained message (not a separate message —
   that would risk two consecutive same-role messages, which not every
   OpenAI-compatible server tolerates). Judging "has the state I'm
   waiting for arrived" only needs recent turns, not the full history
   since the step began, so this is safe to drop.
2. **Size within a single turn**: even with history capped, a *single*
   turn's own PTY output could still overflow on its own — confirmed
   live, one `wait` turn's freshly-read output alone pushed a request
   past 32768 tokens, independent of any accumulated history.
   `truncateOutput` now caps a single turn's shown output to
   `maxSingleTurnOutputChars` (6000), keeping the tail (the current
   state is what matters, not what churned past before settling) with a
   note if anything was cut. The full untruncated output still goes into
   the `-json` transcript (`TurnLog.PTYOutput`) — only what the *model*
   is shown gets bounded.

Covered by `TestStepHistoryIsTrimmedAfterManyTurns` and
`TestSingleTurnOutputIsTruncated` in `internal/runner/runner_test.go`.
Both fixes were necessary — after fixing only the first, the very next
run still aborted, this time from the second cause alone.

### Finding: Claude Code's numbered menu options are not keyboard shortcuts

Real agentic work triggers a per-file edit-permission prompt (unless
already granted) — "Do you want to proceed? 1. Yes  2. Yes, and always
allow...  3. No" — which the original spec hadn't anticipated. The
model tried selecting option 2 by sending the digit `"2"` plus Enter.
Nothing happened; the file was never created. Verified directly against
a raw PTY, independent of the harness: sending `"2\r"` produced no
visible effect and the target file never appeared, while sending a real
Down-arrow escape sequence (`\x1b[B`) followed by `\r` correctly moved
the highlighted `❯` cursor to option 2 and confirmed it — the file was
created immediately after. **The numbers are position labels for a
human reading the screen, not keyboard shortcuts** — Claude Code's TUI
menus (this one and the trust prompt earlier) only respond to real
arrow-key navigation plus Enter. A bare Enter accepts whatever is
already highlighted (which is why the trust step's "press Enter alone"
fix worked without ever needing arrow keys — the default there was
already the wanted option); selecting a *non-default* option needs an
actual Down-arrow keystroke first. Not a harness bug — the harness
faithfully sent whatever bytes it was told to — worked around at the
spec level: step 6's hint now tells the model to send a literal Down-arrow escape (written as `\u001b[B`) then an
empty-command Enter rather than a digit.

### Finding: an occasional model tendency to answer a prompt one step early

Step 2 only asks the model to confirm the TUI launched and the trust
menu is *visible* — answering it is step 3's job. Across the first six
runs, step 2 failed 3 times, always the same way: on seeing the trust
menu, the model tried to answer it immediately (once via a digit key,
once via a digit key with an accidental embedded `\n`), and when nothing
visible happened (per the finding above), concluded the `claude` launch
itself must have failed and spiraled into re-running `claude` and
unrelated shell probing until the turn budget ran out. This is a
genuine model behavior, not a harness bug or a bad step definition —
seeing an actionable prompt and eagerly trying to resolve it is a
plausible completion for a model whose training rewards being generally
helpful, even though it wasn't asked to act yet. Worked around at the
spec level: step 2's `Expect` now explicitly says not to answer the
trust question in this step, and that `claude` appearing to do nothing
once the menu is visible is normal, not stuck. 0 failures in the two
runs since.

### Result

Two clean, complete 11/11 runs against Qwen3.5-9B at temp=0.1 after all
of the above — a real trust decision, a real multi-part task, a real
tracked task list, real completion of real agentic work (including
correctly navigating an unplanned-for permission prompt), a clean exit,
and three separate ground-truth checks that all genuinely passed:
`greet.py`'s contents were correct, running it printed exactly
"Hello, World!", and `pytest` reported "1 passed" for `test_greet.py`.
None of this was taken on the TUI's word — every claim in the report is
backed by a real command run against the real filesystem afterward.

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

This log currently covers two model families (Qwen2.5, and one xLAM
data point), one local runtime (llama.cpp), small sizes only, and four
specs, with limited repeated runs. Every fix above is a fix for the
specific failure mode one specific model happened to hit. The principles
extracted (tolerate and state the correction rather than reject; never
let a nudge name a verdict; end
specs with a ground-truth step) are believed to generalize, but that is a
belief, not something this log has tested at scale. Widening the sample —
other model families, other quantisations, repeated runs of the same
spec — is the natural way to stress-test that belief.

## mlx-lm vs llama.cpp: same model, different quantization schemes, different reliability

Same weights (`Qwen3.5-9B`), same task, two different local inference
stacks — `llama.cpp` (`unsloth/Qwen3.5-9B-GGUF:Q4_K_M`, the model this
whole log's later entries are built around) versus `mlx-lm`
(`mlx-community`'s MLX conversions). Investigated because `mlx-lm`
measured 1.5-3x faster token generation on this hardware — worth
knowing whether that speed is free or costs something.

### Setup gotchas, found before any reliability testing was possible

- **`mlx_lm.server` defaults to thinking mode on, with `--max-tokens 512`.**
  The model's real answer lands in a `reasoning` field; `content` comes
  back empty or truncated mid-thought (`finish_reason: "length"`) at the
  default token budget. `slmtest` only reads `message.content`, so every
  turn would fail to parse without `--chat-template-args
  '{"enable_thinking":false}'`. Confirmed at both `max_tokens: 50` and
  `max_tokens: 500` — 500 tokens of pure reasoning still wasn't enough
  to reach an answer for a two-word JSON reply.
- **`mlx_lm.server` completely ignores `response_format`.** Grepped its
  source directly (`server.py`): zero references to
  `response_format`/`json_object`/`json_schema`/`grammar`. `llama.cpp`
  honors `response_format: {"type": "json_object"}` via grammar-
  constrained decoding (see "The agent contract" in `CLAUDE.md`); `mlx-lm`
  gives no equivalent server-side guarantee. Not the root cause of the
  reliability gap below (every reply captured was syntactically valid
  JSON — the mistakes were semantic, not malformed syntax), but a real
  difference worth knowing before assuming `mlx-lm` is a drop-in
  equivalent for a task shape this schema-sensitive.
- **Speculative decoding fails outright for this model.**
  `mlx_lm.server --draft-model mlx-community/Qwen3.5-0.8B-4bit` against
  the `Qwen3.5-9B-8bit` target errors immediately: `ValueError:
  Speculative decoding requires a trimmable prompt cache (got
  {'ArraysCache'})`. Qwen3.5's novel hybrid Gated DeltaNet + MoE
  architecture (see the Qwen3.5-9B entry above) uses a cache type
  `mlx-lm`'s speculative-decoding implementation can't roll back —
  an architecture-level incompatibility, not a flag to work around.
- **`mlx_lm.server` has no `--kv-bits`/`--kv-group-size` flags** — quantized
  KV cache is `mlx_lm.generate`-only, reachable through the Python API but
  not the server CLI this project drives over HTTP.
- **Prompt caching works, and matters for this harness's shape.**
  `slmtest`'s per-step history resends a growing prefix every turn (each
  turn's request is the previous one plus one more exchange) — exactly
  what KV-cache reuse is for. Confirmed live: a follow-up request sharing
  a prefix with an earlier one reused 31 of 54 prompt tokens
  (`usage.prompt_tokens_details.cached_tokens`). `--prompt-cache-size 8`
  costs nothing and is a straightforward win for this access pattern.

### The reliability gap: 4-bit measurably degrades multi-step reasoning here

With thinking disabled, `mlx-community/Qwen3.5-9B-4bit` (plain RTN —
"a repo name ending in `-4bit` is almost certainly the data-free RTN
convert" per `mlx-lm`'s own
[`LEARNED_QUANTS.md`](https://github.com/ml-explore/mlx-lm/blob/main/mlx_lm/LEARNED_QUANTS.md))
failed `tui-editor-test.md` on **every attempt**:

- Default temperature (0.1): failed at step 3 ("Leave insert mode"),
  8/8 turns exhausted.
- Temperature raised to 0.7 (twice, matching Qwen3.5's own model-card
  recommendation for non-thinking mode): failed both times — once at
  step 2, once at step 3. **Temperature had no effect on the failure
  mode**, which rules out sampling variance as the explanation.
- A second, independently-quantized 4-bit MLX build
  (`mlx-community/Qwen3.5-9B-MLX-4bit`, ~5.06 bits/weight, a different
  conversion from the plain `-4bit` repo) also failed — at step 2 this
  time. Two different 4-bit-class MLX quantizations, two different
  failure points, same outcome: this points at bit-depth, not a
  specific conversion recipe.

The failure mode itself is worth stating precisely, because it is not a
JSON-formatting slip — reading the full transcript (`-json`) at 0.7
temperature: the model correctly entered insert mode, correctly typed
the text, then reasoned that pressing Enter would "confirm the input
line in vi" — which is wrong (Enter in insert mode inserts a literal
newline; it does not exit insert mode or "confirm" anything) — and from
there lost track of vi's mode state entirely, eventually trying
`run_command: ls` while vi was still open in insert mode. That is a
**reasoning** failure about what the editor's current state actually is,
not a schema-following failure. (A distinct, secondary formatting
mistake — nesting `send_keys`'s `command` field under `"params"`,
`send_keys`/`run_command` being the one exception to that convention —
also recurred across turns and was recoverable each time via
`driver.BadParamsError`'s feedback loop; it just wasn't the thing that
sank the step.)

**`mlx-community/Qwen3.5-9B-8bit` cleared the same spec cleanly four
separate times** — three full 6/6 passes of `tui-editor-test.md`, plus a
clean 5/5 on `workspace-test.md` — with thinking still disabled and no
other setting changed. The precision difference (8-bit vs the 4-bit
family) is the variable that actually explains the gap, not temperature,
not the specific 4-bit conversion, and not `response_format`.

### Thinking mode, with an adequate token budget, also fixes it — at a real cost

Re-enabling thinking on the plain RTN 4-bit quant (default chat-template
args, `--max-tokens 3000` so the model has room to actually finish
reasoning before answering) also cleared `tui-editor-test.md` 6/6,
including the exact step ("Leave insert mode") that failed without it —
consistent with the failure being a state-tracking reasoning gap that
extra deliberation compensates for. The cost: ~15-20s per turn instead of
~1s, which erases most of the raw speed advantage 4-bit was chosen for
in the first place. Not recommended as the default for this reason — see
"Do not reach for the 4-bit quant to go faster" above.

### Why llama.cpp's Q4_K_M didn't show the same degradation

Two architectural differences, not one:

1. **GGUF's `Q4_K_M` is mixed-precision, not uniform 4-bit** — most
   tensors at 4 bits, but key tensors (attention/output) kept at 6 bits.
   `mlx-community`'s plain `-4bit` repos are uniform, uncalibrated RTN
   across every tensor.
2. **unsloth's GGUF build applies its own calibration** on top of the
   k-quant scheme (their "Dynamic" quants), which the plain MLX `-4bit`
   conversion does not.

Both differences point the same direction: `Q4_K_M`'s effective fidelity
at "4-bit" is higher than a naive uniform 4-bit MLX conversion, and that
shows up specifically on tasks demanding multi-step state-tracking
(editor modal state, in this case) rather than on simpler single-turn
correctness — which is exactly the class of task this project's harder
specs (`tui-editor-test.md` especially) were built to probe.

### Speed measured, for the record

Direct completion-token throughput, same prompt (`"Write a 150 word
paragraph about terminal automation testing"`, `max_tokens: 200`), same
hardware, thinking disabled where applicable:

| Setup | Tokens/sec |
|---|---|
| `llama.cpp`, `Qwen3.5-9B-GGUF:Q4_K_M` (this log's earlier entries) | ~10-11 |
| `mlx-lm`, `Qwen3.5-9B-8bit` | ~15.3 |
| `mlx-lm`, `Qwen3.5-9B-4bit` (RTN) | ~20.6 |
| `mlx-lm`, `Qwen3.5-9B-MLX-4bit` | ~27.7 |

The 4-bit numbers are real, but per the reliability findings above, not
usable without either accepting the failure rate or paying it back with
thinking mode's latency — at which point 8-bit is both faster and more
reliable. **8-bit is the recommended default**: ~40% faster than
`llama.cpp` at equal-or-better reliability, no caveats.

### Per-spec timing on the recommended setup

Raw tok/sec (above) is a micro-benchmark on one fixed prompt; what
actually matters is real spec runs. Fresh, sequential runs (no
concurrent contention skewing the numbers) against the standing
recommended config (`mlx-lm`, `Qwen3.5-9B-8bit`, thinking off,
`--prompt-cache-size 8`):

| Spec | Steps | Turns | Total time | Avg sec/step |
|---|---|---|---|---|
| `echo-test.md` | 1 | 2 | 7.2s | 7.2s |
| `driver-frontmatter-test.md` | 1 | 2 | 5.8s | 5.8s |
| `workspace-test.md` | 5 | 13 | 49.9s | 10.0s |
| `tui-editor-test.md` | 6 | 16 | 61.1s | 10.2s |
| `browser-test.md` | 2 | 4 | 17.7s | 8.9s |
| `browser-form-test.md` | 5 | 13 | 38.9s | 7.8s |

All six passed cleanly (100%, matching every repeated verification run
earlier in this section). Two things worth reading correctly here:

- **Seconds/step tracks turns/step more than raw model speed.**
  `workspace-test.md` and `tui-editor-test.md` average ~2.6-2.7
  turns/step (multi-step reasoning, occasional self-correcting
  retries), while the single-step specs finish in one round-trip. The
  per-turn cost itself is fairly flat, roughly 2.5-3s, consistent with
  the ~15 tok/s measured directly above.
- **The browser specs carry fixed Chromium startup/teardown overhead**
  baked into the total (Playwright launching and closing a real browser
  each run), so their sec/step isn't purely model latency —
  `browser-test.md`'s 2 steps in 17.7s is proportionally slower than
  `browser-form-test.md`'s 5 steps in 38.9s for exactly that reason: the
  fixed cost is amortized over more steps in the second case.

### MTPLX: a faster sustained-decode benchmark that doesn't translate to a faster harness here

[MTPLX](https://github.com/mtplx) (`v2.10.1`) adds native MTP
(multi-token-prediction) speculative decoding on top of MLX, using the
model's own trained MTP heads rather than a separate draft model —
sidestepping the architecture incompatibility that killed classic
speculative decoding for this model (see "Speculative decoding fails
outright for this model" above). Investigated because its own reported
sustained-decode benchmark on this hardware showed a real win:

| Setup | Sustained decode speed |
|---|---|
| `llama-server` (`Q4_K_M`) | 20.7 tok/s |
| `mlx-lm`, plain 4-bit | 32.7-33.7 tok/s |
| MTPLX, autoregressive baseline (same pack, MTP off) | 21.3 tok/s |
| MTPLX, D3 native MTP speculative decoding | 39.8 tok/s — 1.87x its own AR baseline |

MTPLX's own AR baseline (21.3 tok/s) is *slower* than plain `mlx-lm`
4-bit (33.7 tok/s): its published weights
(`Youssofal/Qwen3.5-9B-MTPLX-Optimized-Speed`, 8.7GB — closer to 8-bit
precision than `mlx-community`'s 5.2GB plain 4-bit pack) need that extra
precision to keep the MTP draft/verify pass numerically exact. With
drafting on (depth 3, auto-selected by `mtplx tune` as best for this
Mac), it pulls ahead of plain `mlx-lm` 4-bit anyway — 39.8 vs 33.7
tok/s — without needing a separate draft model. Output distribution is
unchanged (exact rejection sampling, not a lossy approximation) —
confirmed by this project's own reliability suite below, not just a spot
check.

**One real setup difference from plain `mlx-lm`, worth knowing before
assuming API parity:** MTPLX actually *enforces*
`response_format: {"type": "json_object"}` (via the optional
`llguidance` dependency — `pip install llguidance`, then restart), where
plain `mlx_lm.server` silently ignores the field entirely (see above).
Without `llguidance` installed, MTPLX refuses the request outright
rather than silently returning unconstrained output — the same "state
precisely what's wrong" instinct behind this project's own design.

**Reliability, run against this project's own suite: no regression at
all.** `tui-editor-test.md` 6/6, twice; `workspace-test.md` 5/5; every
other spec clean. Confirms the exact-rejection-sampling claim in
practice, not just in theory.

**But real per-spec timing on `slmtest`'s actual workload is slower than
plain `mlx-lm` 8-bit, not faster** — the headline sustained-decode number
does not transfer:

| Spec | Steps/Turns | `mlx-lm` 8-bit | MTPLX (D3) |
|---|---|---|---|
| `echo-test.md` | 1/2 | 7.2s | 12.1s |
| `driver-frontmatter-test.md` | 1/2 | 5.8s | 4.7s |
| `workspace-test.md` | 5/13 | 49.9s | 47.4s |
| `tui-editor-test.md` | 6/16 | 61.1s | 65.7-69.6s (two runs) |
| `browser-test.md` | 2/4 | 17.7s | 20.3s |
| `browser-form-test.md` | 5/13 | 38.9s | 43.2s |

MTPLX comes out slower or roughly even on 5 of 6 specs. The likely
explanation is the task-shape mismatch: `slmtest` turns are short — a
few dozen tokens of compact JSON per reply — not the long, homogeneous
generations MTP speculative decoding is benchmarked on and where its
draft/verify batching amortizes well. Two costs specifically eat into
its advantage at that granularity: draft/verify overhead has too few
tokens to amortize over per turn, and grammar-constrained decoding for
`response_format` (which MTPLX enforces and plain `mlx-lm` silently
skips) adds real per-token validation cost neither the sustained-decode
benchmark nor plain `mlx-lm` pays.

Tried `--batching-preset solo` (this project's actual access pattern is
genuinely one request in flight at a time, not the concurrent
coding-agent load the default batching presets target) as the one
tuning knob most likely to help a short-turn workload specifically:
67.1s for `tui-editor-test.md`, squarely inside the untuned baseline's
65.7-69.6s range — no meaningful change. Temperature was already ruled
out as a lever earlier in this section (against plain `mlx-lm`'s 4-bit
quant, at both 0.1 and 0.7), and thinking mode was off throughout these
runs by design (see "Setup gotchas" above) — re-enabling it would only
widen the latency gap further, the same tradeoff already measured
against `mlx-lm`'s 4-bit quant. No combination tried closes the gap for
this task shape.

**Conclusion: `mlx-lm` + `Qwen3.5-9B-8bit` remains the recommendation**
for this project specifically. MTPLX's speed advantage is real for its
own benchmark shape but doesn't hold for `slmtest`'s short,
schema-constrained agentic turns — a reminder that a sustained-decode
tok/sec number is not a substitute for measuring the actual workload.

### The full local-vs-remote picture: same specs, four backends

Rounding out the comparison with `DeepSeek-V4-Flash` (this log's "large
model, quality reference" — see "How to run this yourself" above),
reached over the same fresh, sequential, per-spec-timed methodology used
throughout this section:

| Spec | `llama.cpp` (local, `Q4_K_M`) | `mlx-lm` 8-bit (local) | MTPLX D3 (local) | `DeepSeek-V4-Flash` (remote) |
|---|---|---|---|---|
| `echo-test.md` | 16.8s | 7.2s | 12.1s | **3.8s** |
| `driver-frontmatter-test.md` | 11.0s | 5.8s | 4.7s | **3.6s** |
| `workspace-test.md` | 104.6s | 49.9s | 47.4s | **24.0s** |
| `tui-editor-test.md` | — | 61.1s | 65.7-69.6s | **30.6s** |
| `browser-test.md` | — | 17.7s | 20.3s | **10.6s** |
| `browser-form-test.md` | — | 38.9s | 43.2s | **23.8s** |

(`llama.cpp`'s `tui-editor-test.md`/browser-spec cells are blank —
those weren't freshly re-timed with this methodology this session, only
confirmed pass/fail earlier in this log.)

All six specs passed cleanly against `DeepSeek-V4-Flash`. It's
**~2x faster than `mlx-lm` 8-bit and ~3-4x faster than `llama.cpp`** on
the specs where all backends were measured — unsurprising, since it's a
large model on server-grade hardware rather than a 9B model on a laptop.
This isn't a recommendation to switch: it's a reminder of what the
`-endpoint`/`-model` flags are for. `mlx-lm` + `Qwen3.5-9B-8bit` remains
the right choice for local, offline, zero-marginal-cost iteration; a
hosted large model is the right choice when wall-clock time matters more
than that, and the harness makes switching between them a one-flag
change with no code involved either way.

## `press_key`'s real-model verification, closing a gap left open by the driver-abstraction refactor

The driver-abstraction plan (see `internal/driver`'s doc comments) called
for confirming `press_key`-based arrow-key/Enter navigation against a
real model driving `tui-claude-test.md`'s trust-prompt menu before
treating it as equivalent to the old raw-escape-sequence approach — this
had not actually been done. Updated the spec's step 4 to use `press_key`
(`{"key": "escape"}`, with the `send_keys` Ctrl-C fallback kept) and ran
it against `mlx-lm`/`Qwen3.5-9B-8bit` twice.

**`press_key` works correctly, consistently, both runs**: the model
tried `{"action":"press_key","params":{"target":"down"}}` first (the
wrong field name — conflating it with `click`'s `target` param), got a
clean, recoverable `driver.BadParamsError`, self-corrected to
`{"key":"down"}` on the very next turn, and the down-arrow **genuinely
moved the `❯` marker** from "Yes, I trust this folder" to "No, exit" —
confirmed in the raw terminal output, not just inferred. `press_key:
enter` then confirmed the selection and the TUI exited cleanly to a bash
prompt. This is the same behavior CLAUDE.md already documents for the
raw escape-sequence approach (real arrow-key bytes move the highlighted
option; digit keys do not) — `press_key` reproduces it via the logical
key name, as designed.

**A separate, unrelated finding, also consistent across both runs**: the
model completed step 4's entire interaction (decline, confirm) *while
still inside step 2's turn budget* — step 2's own Goal is only "confirm
the TUI launched and drew its interface," but the model kept going past
that and tried to navigate the trust menu immediately, then got
confused when it needed to re-observe for step 2's own verdict (having
already exited the TUI its own actions closed). Both runs failed
step 2 for exactly this reason, never reaching step 4 as their own
step — even though step 4's *mechanics* had already been executed
correctly, just under the wrong step. This is a spec-sequencing /
model-eagerness issue, not a `press_key` or schema bug, and is a
different failure shape from anything else in this log — worth a
closer look if this spec keeps getting picked up for further real-model
runs, but out of scope for what this section set out to verify.
