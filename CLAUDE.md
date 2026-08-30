# CLAUDE.md — slmtest

This file orients an LLM (or a human) working in this repository: what the
tool is, how the pieces fit together, and — critically — how to *write* a
test spec and how to *run* one. If you're an assistant helping the user
either build out this scaffold or author test files against it, read this
whole file before doing either.

## What this is

`slmtest` runs interactive terminal tests against a small language model
(SLM). A test is a plain markdown file: a short frontmatter block plus a
list of numbered steps, each with a **Goal**, an optional **Hint**, and an
**Expect** criterion. The tool spawns a real shell inside a pseudo-terminal
(PTY), then for each step it loops: show the model the step + recent
terminal output, get back one structured action (run a command, wait, or
declare the step passed/failed), execute it, repeat — until the model
reaches a verdict or a turn/time budget runs out.

This is deliberately close to how a human QA engineer runs a manual test
script: read the step, try the suggested command, look at what happened,
decide whether it worked, adapt if it didn't, move to the next step.

**This is not a benchmark.** It's an automation harness — the closest prior
art is [Terminal-Bench](https://github.com/laude-institute/terminal-bench)
(task = instruction + Docker env + test script + oracle solution, agent
drives a tmux session, verified against **final container state**). This
tool differs from that in two load-bearing ways:

1. **Step-level pass/fail, not just final-state checking.** Terminal-Bench
   only knows whether the whole task succeeded at the end. Here, each step
   gets its own verdict and reason, because the goal is a QA-style test
   report ("step 3 failed: nginx config missing"), not a single pass/fail
   bit.
2. **Markdown is the spec.** Terminal-Bench splits instruction text and
   test code into separate files/languages. Here, the goal, the hint, and
   the success criterion live together in one human-readable, model-
   readable document.

## Repository layout

```
cmd/slmtest/main.go       CLI entrypoint (run / validate / init)
internal/spec/spec.go     markdown → Test struct parser
internal/agent/           SLM client + the JSON action schema/contract
internal/ptydriver/       PTY process management (creack/pty wrapper)
internal/sandbox/         macOS Seatbelt profile generation
internal/runner/          the per-step turn loop that ties it all together
examples/                 sample test specs + a mock SLM server for smoke tests
.github/workflows/ci.yml  build/vet/gofmt/test on macOS + Linux, plus an end-to-end smoke run
```

Read them in that order if you're new to the code — each layer only
depends on the ones before it (`spec` has no dependencies; `runner`
depends on all three).

## Building

```
go build -o slmtest ./cmd/slmtest
```

Only external dependency: `github.com/creack/pty`. No YAML library —
frontmatter is deliberately simple `key: value` lines (see below), parsed
by hand, so the tool has zero exotic dependencies.

## The markdown test-spec format

```markdown
---
name: nginx-smoke-test
description: Verify nginx installs, starts, and serves the default page
shell: /bin/bash
timeout_seconds: 300
max_turns_per_step: 8
---

## Step 1: Install nginx
Goal: nginx is installed and the binary is on PATH.
Hint: apt-get update && apt-get install -y nginx
Expect: `nginx -v` exits 0 and prints a version string.

## Step 2: Start the nginx service
Goal: the nginx service is running and listening on port 80.
Hint: service nginx start
Expect: curl to localhost:80 returns HTTP 200.
```

**Frontmatter fields:**

| Field | Required | Meaning |
|---|---|---|
| `name` | yes | test identifier, shown in reports |
| `description` | no | one line, human-facing |
| `shell` | no | shell to launch (default `/bin/sh`) |
| `timeout_seconds` | no | whole-test wall-clock budget (0 = unlimited) |
| `max_turns_per_step` | no | reasoning-turn budget per step (default 6) |
| `term` | no | value of `TERM` in the shell's environment; empty inherits the parent's |
| `size` | no | terminal size as `ROWSxCOLS`, e.g. `40x200` (the default) |

**Step fields** (each step is a `## Step N: Title` heading):

- **Goal** (required) — plain-language description of the state the system
  should be in after this step. This is what the model is ultimately
  reasoning toward.
- **Hint** (optional) — a *suggested* command. Not a script. The model is
  explicitly told a hint is not authoritative — if it fails, the model
  should reason about why (missing package, wrong path, needs sudo, needs
  a retry after a service starts) rather than immediately failing the
  step. This is the "flexibility to reason how to get to the next step"
  the tool is built around.
- **Expect** (required) — the concrete, checkable condition that means the
  step passed. Write this so a human could grade it just by reading
  terminal output — that's exactly what the SLM is being asked to do.
- **Size** (optional) — `ROWSxCOLS` for this step only, e.g. `24x80`.
  Only needed for a step driving something that reflows (a TUI, a pager,
  a wide table). The terminal reverts to the test's size for the next
  step, so one TUI step doesn't silently reshape the rest of the run.
  Note the ordering is rows first, matching `stty` and `pty.Winsize`
  rather than the WIDTHxHEIGHT convention of image tooling.

### Writing good steps (for whoever/whatever authors the `.md`)

- **One observable outcome per step.** "Install and start nginx" is two
  steps, not one — if it fails, you want to know *which half* broke.
- **Make Expect checkable from output, not from side knowledge.** "the
  service should be healthy" is vague; "`curl` returns HTTP 200" is not.
- **Don't overload Hint as a full script.** A single representative command
  is enough — a wall of `&&`-chained commands removes the model's room to
  adapt when step 2 of the chain is what actually fails.
- **Steps run in order, and by default the run stops at the first
  failure** (later steps usually assume earlier ones succeeded — a service
  that never started, a file that was never created). Pass
  `-continue-on-fail` to attempt every step regardless. That's a run-time
  flag rather than a spec-file field on purpose: CI usually wants the full
  picture, while someone iterating locally wants the fast exit, and that's
  a property of the run, not of the test. Note that under
  `-continue-on-fail` the PTY keeps whatever state the failed step left
  behind, so later steps run against it.

## The agent contract (JSON action schema)

Every model turn must reply with exactly one JSON object, nothing else:

```json
{
  "thought": "optional one-sentence reasoning, for logs only",
  "action": "run_command | send_keys | wait | finish_step | abort_test",
  "command": "shell text — required for run_command/send_keys",
  "press_enter": true,
  "wait_ms": 1500,
  "step_result": "pass | fail",
  "reason": "required for finish_step and abort_test"
}
```

- `run_command` — type `command`, press Enter, wait `wait_ms` (default
  1500ms), then the harness shows the model whatever new output appeared.
- `send_keys` — like `run_command` but does **not** press Enter by
  default. For interactive programs, partial input, or control characters
  (e.g. `"\u0003"` for Ctrl-C).
- `wait` — no terminal action, just wait and re-observe. For long-running
  commands (builds, downloads, service startup) that need more time.
- `finish_step` — the only way a step ends. Requires `step_result` and a
  `reason`. The harness never infers pass/fail on its own from exit codes;
  it surfaces them to the model and lets it decide, but this means the
  system prompt's instruction *"judge only by output you can actually
  see, don't guess pass"* matters a lot for a small model — see
  `internal/runner/runner.go`'s `systemPrompt` constant for the exact
  wording in use.
- `abort_test` — ends the whole run immediately. Reserved for a broken
  environment (PTY died, container unusable), not a normal step failure.

**Small-model robustness notes** (why the schema/parser look the way they
do):

- The parser tolerates a model wrapping its JSON in a ` ```json ` fence —
  small models do this reliably even when told not to (see
  `internal/agent/fence.go`).
- A malformed reply is **not** fatal. The runner sends the exact parse
  error back to the model as the next turn ("your reply could not be
  parsed: ...") and gives it another shot, within the same turn budget.
  This one design choice matters more than any prompt wording for SLM
  reliability — see the Terminal-Bench error analysis referenced below:
  command/format errors dominate small-model failures, and most are
  one-shot recoverable if you tell the model exactly what was wrong.
- The model's own `thought` field is **not** replayed back into its own
  context on later turns — only the actual PTY output is kept in history.
  This keeps context small and stops the model from reasoning about its
  own past reasoning instead of the terminal state.
- `response_format: {"type": "json_object"}` is sent on every request —
  honored by llama.cpp, vLLM, and newer OpenAI-compatible servers to
  constrain output to valid JSON. Harmless if the server under test
  ignores it, since the fence-stripping parser is the real safety net.

## Running a test

```
slmtest run <file.md> [flags]
```

| Flag | Default | Meaning |
|---|---|---|
| `-endpoint` | `http://localhost:8080/v1` | OpenAI-compatible base URL (works with llama.cpp server, Ollama's OpenAI-compat mode, vLLM, LM Studio, or a hosted API) |
| `-model` | `local-slm` | model name sent in the request body |
| `-api-key` | (empty) | bearer token, if the endpoint needs one |
| `-shell` | (spec's `shell` field) | override the shell launched in the PTY |
| `-json` | off | print the final report as JSON (for CI / tooling) instead of the human-readable summary |
| `-verbose` | off | stream each turn (prompt, reply, PTY output) to stderr as it happens |
| `-step-timeout` | `0` | per-step wall-clock budget (e.g. `90s`); 0 = no limit. Distinct from the spec's `timeout_seconds`, which bounds the whole run |
| `-command-wait-ms` | `0` | default wait after a command when the model omits `wait_ms` (0 = the built-in 1500ms) |
| `-continue-on-fail` | off | attempt every step even after one fails (see below) |
| `-max-retries` | `3` | attempts per SLM request before the run aborts; `1` disables retrying |
| `-request-timeout` | `2m` | timeout for a single model request; raise it for slow or CPU-only models |
| `-sandbox` | off | confine the shell with macOS Seatbelt (see below) |
| `-sandbox-write` | (none) | with `-sandbox`, an extra writable path; repeatable |
| `-sandbox-deny-network` | off | with `-sandbox`, also block all network access |
| `-sandbox-profile` | (empty) | with `-sandbox`, a hand-written `.sb` profile to use instead of the generated one |
| `-exec-prefix` | (empty) | wrap the shell in an arbitrary command, e.g. `"ssh testbox"`; mutually exclusive with `-sandbox` |

**Flags go after the file path**, matching the documented usage
(`slmtest run <file.md> [flags]`) — this is enforced explicitly in
`main.go`'s `takeLeadingPositional`, because Go's stdlib `flag` package
stops parsing at the first non-flag token and will otherwise silently
swallow flags placed after a positional argument.

Exit code is `0` if every step passed, `1` otherwise (including aborts) —
safe to use directly in CI.

### The `-json` report shape

`-json` is a CI contract, so it has an explicit shape (`Report.MarshalJSON`
in `internal/runner/runner.go`) rather than whatever the Go structs happen
to serialize to. Two deliberate differences from the in-memory `Report`:
each step's spec fields are flattened into its outcome, and the parsed
`Test` is not echoed wholesale (its `Steps` would duplicate everything
already under `steps`).

```json
{
  "name": "echo-smoke-test",
  "description": "...",
  "passed": true,
  "aborted": false,
  "steps": [
    {
      "index": 1, "title": "...", "goal": "...", "hint": "...", "expect": "...",
      "status": "pass",
      "reason": "saw hello-from-pty in terminal output",
      "turns": 2,
      "transcript": [
        {"user_prompt": "...", "raw_reply": "...", "action": {...}, "pty_output": "..."}
      ]
    }
  ]
}
```

`status` is one of `pass` / `fail` / `timeout` / `abort`, from
`StepOutcome.Status()`. It exists because the several independent fields
on `StepOutcome` collapse to one distinction a reader acts on, and because
the human and JSON reports must never drift apart — `printReport`
uppercases the same value. The four are meaningfully different:
`timeout` means the harness gave up waiting, and `abort` means the run
could not continue at all (dead PTY, unusable endpoint) — neither says
the system under test failed. A turn whose reply never parsed has no
`action` key at all, just `raw_reply` and `error`.

Other commands:

```
slmtest validate <file.md>   # parse-check only, no execution, no model call
slmtest init <file.md>       # write a starter template to file.md
```

## The example specs

| File | Runs on | Purpose |
|---|---|---|
| `echo-test.md` | anywhere | one step; the smoke test the mock server is built for |
| `workspace-test.md` | anywhere, incl. `-sandbox` | five steps of real filesystem work; the realistic end-to-end demo |
| `nginx-smoke-test.md` | Linux with apt | aspirational — illustrates the format, does not run on macOS |

`workspace-test.md` step 4 deliberately passes either way: it asks the
model to try a write outside the workspace and *report which happened*,
so the same spec documents the difference `-sandbox` makes rather than
needing two variants.

## Smoke-testing the harness itself (no real SLM needed)

`examples/mock_slm_server.py` is a tiny deterministic OpenAI-compatible
server used to verify the harness's own plumbing (PTY, parsing, turn loop)
without needing a real model running. It only completes a step once it
sees the expected string in **actual terminal output** — not in the
prompt text — which is worth preserving as a pattern if you add more
smoke tests: a naive mock that pattern-matches the prompt itself can pass
without ever touching the PTY, which defeats the point.

```
python3 examples/mock_slm_server.py &
go build -o slmtest ./cmd/slmtest
./slmtest run examples/echo-test.md -endpoint http://localhost:8080/v1 -verbose
```

## The Go test suite

```
go test ./...          # ~15s, mostly PTY wait time
go test -race ./...     # clean
```

Unit tests live beside each package. What they cover, and why those
choices:

- `internal/spec` — the format contract: defaults, per-position step
  indexing, tolerance for `**Goal:**`-style emphasis, and every parse
  error the CLI can surface to a spec author.
- `internal/agent` — `ParseAction` against the small-model failure modes
  the design anticipates (prose instead of JSON, code fences, wrong
  action names, missing required fields), plus `Complete`'s request
  shape and endpoint-error handling via `httptest`.
- `internal/ptydriver` — drives a **real** `/bin/sh` in a **real** PTY.
  Mocking the terminal would leave the only interesting behavior
  untested, so these are genuine integration tests: new-output-only
  snapshots, Enter vs. no-Enter, exit codes reaching the model, `Alive()`
  flipping on shell exit, context cancellation.
- `internal/runner` — the turn loop against a scripted fake SLM
  (`fakeSLM` in `runner_test.go`) plus a real PTY. Covers the behaviors
  that are load-bearing but easy to regress silently: parse errors
  costing a turn rather than the run, per-step history reset,
  stop-on-first-failure, abort vs. failure, turn-budget exhaustion, and
  the `thought`-not-replayed invariant.

The fake SLM deliberately fails the test if the runner asks for more
turns than its script provides — an unexpected extra model call is a bug
worth surfacing loudly rather than absorbing.

## Running a model to test against

The harness only ever speaks OpenAI-compatible HTTP, so "which model"
is a matter of what you point `-endpoint` at. Two setups are worth
keeping to hand, because they answer different questions.

**A small model locally**, which is the regime this tool is designed for:

```
llama-server -hf Qwen/Qwen2.5-1.5B-Instruct-GGUF:Q4_K_M --port 8080 -c 8192
slmtest run examples/workspace-test.md -endpoint http://localhost:8080/v1 -request-timeout 5m
```

`llama-server` is one binary with no daemon and fetches the weights
itself, which is why it is preferred here over a model runner that wants
a background service and its own model store.

**A large model**, as a quality reference — if a step fails, running the
same spec against a stronger model tells you whether the spec is
ambiguous or the small model is the limit:

```
slmtest run examples/workspace-test.md -endpoint http://localhost:8888/v1 -model <name>
```

There are deliberately **no llama.cpp bindings** in this repo. A cgo
binding would bridge nothing that HTTP does not already cover, while
costing a C++ toolchain in the build, platform-specific acceleration
flags, and a CI matrix that currently just works. If launching the server
ever needs to be automatic, the honest form is a small `os/exec`
supervisor around `llama-server`, not a linked library.

Expect small models to need a longer `-request-timeout` than large hosted
ones — the cost is thinking time, not network.

## What running against real models has shown

Almost everything else here is verified against `mock_slm_server.py`, a
deterministic stand-in that answers identically no matter what it is
asked. Two real endpoints have now been used, and they found different
things.

### Against a large model (vLLM, DeepSeek-V4-Flash)

`workspace-test.md` passes 5/5 under `-sandbox`, including the step that
asks the model to notice a write was refused. Replies parsed first time,
no code fence. It found exactly one bug: a **60s request timeout** that a
large-context endpoint could legitimately exceed, made worse by the retry
ladder spending three minutes rediscovering it. Hence `-request-timeout`.

A large model getting it right first time is *not* evidence that the
small-model machinery works. It is evidence it wasn't needed.

### Against a small model (llama.cpp, Qwen2.5-1.5B-Instruct Q4_K_M)

This is the regime the tool was designed for, and it found three real
defects in a single one-step test. All three were invisible to both the
mock and the large model.

**1. `press_enter: false` on `run_command` was a silent no-op.** The model
set the field, the harness honored it, so the command was typed but never
executed. No output ever appeared, and the model spent its entire budget
waiting for a result that could not arrive. `run_command` is *defined* as
"type and press Enter", so the field is now ignored there and honored only
for `send_keys`. **A field whose misuse produces silence rather than an
error is a design bug**, and only a model naive enough to misuse it would
ever reveal that.

**2. It repeated an identical correct command against unchanged output.**
The Expect criterion was plainly satisfied in front of it and it never
drew the conclusion. `repeatNudge` now points this out from the second
repeat: the harness still cannot judge the step (that is the whole
design), but it can name what the model is demonstrably not noticing.

**3. It attached `"step_result": "pass"` to `run_command`** — right
judgement, wrong mechanics — while never calling `finish_step`.

Point 3 is the instructive one, because **the first fix made things
strictly worse**. Rejecting the stray field as a schema error looked
principled and produced a rejection loop: the model resent the same
malformed action every turn, so no command ran at all and the entire
budget went on the argument. Tolerating it and appending a note naming
the mistake — with the exact corrected reply — turned a 4-turn failure
into a 2-turn pass.

With all three fixed, the 1.5B model passes `echo-test.md` in 2 turns and
`workspace-test.md` 5/5 under `-sandbox`.

**Do not read that 5/5 as a clean result.** Inspecting the transcript,
step 2 passed *without verifying anything*: the model ran the write
command, never ran the `cat` its own Expect line called for, and gave as
its reason a sentence lifted verbatim from the prompt's boilerplate ("No
terminal output yet — this is the start of the step"). It reached the
right verdict by luck, not by reading output.

This is the sharpest limitation of the whole design, and it is inherent
rather than fixable by a flag: **the model owns the verdict, so a model
willing to assert an unearned pass will produce one.** The harness
deliberately refuses to infer pass/fail itself, which is what makes it a
QA harness rather than an assertion runner — and that same choice is what
leaves this hole. What the harness does give you is the transcript, where
the false pass is plainly visible. If you care about the result, read it;
a green summary line is not evidence.

An obvious-looking fix — require that a passing step cite terminal output
— was not attempted, because deciding whether cited output actually
supports the Expect criterion is the judgement being delegated in the
first place.

The lesson generalizes, and matches why the fence-stripping parser exists:
**for a small model, tolerate the quirk and state the correction; do not
refuse the turn.** A rejection costs a turn and assumes the model can act
on the refusal. An annotation costs nothing and still delivers the
correction. Reach for `Validate()` when a reply is genuinely unusable, and
for a prompt annotation when it is usable but wrong.

## Known gaps / next steps for whoever extends this

- **A model can assert a pass it did not earn.** Observed with
  Qwen2.5-1.5B: a step passed without the model running the verification
  its own Expect line specified. The harness cannot close this without
  taking over the judgement it exists to delegate (see "What running
  against real models has shown"). Treat a summary line as a claim and
  the transcript as the evidence — `-json` carries the full transcript
  for exactly this reason.
- **Sandboxing is opt-in and confines writes only.** `-sandbox` (macOS
  Seatbelt) is off by default, so without it the shell runs unconfined on
  the host. Even with it, the profile is a deny-list over a shared
  filesystem: reads are unrestricted, and it is not a boundary against
  hostile code. There is no Linux equivalent — `-sandbox` errors there,
  and `-exec-prefix` is the only option. A Landlock or bubblewrap backend
  behind the same `sandbox.Config` would be the natural next step.
- **History is per-step, not per-test.** Each step starts the model's
  chat history fresh (only the system prompt persists) — this keeps
  context small and stops step N's failed attempts from polluting step
  N+1's reasoning. The one exception is a rolling summary of the last
  five step outcomes (`priorSummary` in `internal/runner/runner.go`),
  threaded into each step's *first* user message so a step like "restart
  the service you configured earlier" is answerable. It carries verdicts
  and reasons only — never terminal output, which is precisely what the
  reset exists to discard. It adds no extra messages to the request, and
  is capped so a long spec doesn't grow every prompt without limit.

## Sandboxing

`-sandbox` confines the shell with **macOS Seatbelt** (`sandbox-exec`).
There is deliberately no container runtime involved.

```
slmtest run t.md -sandbox
slmtest run t.md -sandbox -sandbox-write ./workdir -sandbox-deny-network
slmtest run t.md -sandbox -sandbox-profile ./my-profile.sb
```

### Why Seatbelt and not Docker

A container gives stronger isolation and a reproducible filesystem, but
it makes the harness depend on a daemon being installed, running, and
holding a pulled image — three things that fail independently, and none
of which have anything to do with the test being run. During development
of this feature the Docker CLI was present on the machine but its daemon
was not running, which is exactly the failure this avoids.

Seatbelt ships with the OS, starts in microseconds, and needs no daemon.
The cost is that it *confines* the host filesystem rather than replacing
it: a test can still see the whole machine, and "install nginx" means
installing it on the host, not into a disposable image. If you need a
pristine filesystem per run, that's what `-exec-prefix` is for.

### What the generated profile does

It's a deny-list, not an allow-list. Everything is permitted except:

- **Writes outside scratch directories.** `/tmp`, `/var/tmp`, and
  `$TMPDIR` are writable; everything else is not. Add more with
  `-sandbox-write` (repeatable — paths can contain commas, so a
  comma-separated list would be wrong).
- **The network, but only with `-sandbox-deny-network`.** It's allowed by
  default because a test that installs a package or curls a local service
  is the common case.

Reads are untouched. This stops a test from scribbling over your home
directory or system files; it is **not** a security boundary against
hostile code.

A deny-by-default profile was considered and rejected: one that still
lets a shell install packages and start services ends up allowing nearly
everything anyway, while being much harder to read and audit.

### Three things that will bite you writing SBPL by hand

All three were found empirically while building this, and all three fail
*silently* — the profile loads fine and simply doesn't do what it looks
like it does:

1. **Seatbelt matches resolved paths.** `/tmp` is a symlink to
   `/private/tmp`, so a profile granting `(subpath "/tmp")` permits
   nothing at all. `resolvePaths` resolves symlinks for this reason.
2. **`/dev/null` needs an explicit allowance.** A bare `(deny
   file-write*)` turns every `>/dev/null` in every test into "Operation
   not permitted".
3. **`$TMPDIR` is not `/tmp` on macOS.** It points into `/var/folders`,
   and without it `mktemp` fails.

`sandbox-exec` is marked DEPRECATED in its own man page and has been
since 10.8. It is nonetheless present on current macOS (verified on 26.6)
and is what Chrome and similar tools still drive. `sandbox.Available()`
is the single place that would need to change if it is ever removed.

### `-exec-prefix`, for everything else

`-exec-prefix` prepends an arbitrary command to the shell, which covers
whatever Seatbelt doesn't — a container, another machine, a Linux host:

```
slmtest run t.md -exec-prefix "ssh testbox"
slmtest run t.md -exec-prefix "docker run --rm -it ubuntu:24.04" -shell /bin/sh
slmtest run t.md -exec-prefix "apptainer exec image.sif"
```

It is the escape hatch, not the recommended path, and it is the only
sandboxing option on Linux — `-sandbox` fails there with an error saying
so. The prefix *wraps* the shell rather than replacing it, so the spec's
own `shell` field still decides what runs inside.

The prefix is split like a shell would split it — whitespace separates
words; single quotes, double quotes and backslashes group them — but with
**no** variable expansion, globbing, pipes, or substitution (`splitArgs`
in `cmd/slmtest/main.go`). Handing the string to `sh -c` instead would
silently evaluate metacharacters this harness has no business evaluating
on the user's behalf.

`-sandbox` and `-exec-prefix` are mutually exclusive, and the CLI refuses
rather than composing them: `sandbox-exec ... ssh host sh` would confine
the ssh client, not the remote shell it opens.

If you do use a container prefix, note that the `term` frontmatter field
sets `TERM` on the *wrapper* process (the `docker` client), not inside
the container — use `-e TERM=...` in the prefix for that — and that
whether `Driver.Resize` propagates inward is up to the wrapper. Neither
has been verified against a running daemon.

## Retrying the SLM endpoint

Retries live in `agent.Client.Complete`, not in the runner. That's the
load-bearing choice: by the time the runner sees an error from
`Complete`, it genuinely means "this endpoint is unusable", which is
exactly what the runner's abort branch reports it as. Putting retries in
the runner instead would have made every abort ambiguous.

Retried: transport errors (connection refused/reset, DNS, timeout), 5xx,
429, and 408 — a local llama.cpp or Ollama server being restarted looks
exactly like the first of these. Not retried: any other 4xx, a
well-formed `{"error": ...}` body, or a 200 whose body isn't JSON. Those
mean the request itself was rejected, and sending it again unchanged
would get the same answer.

Backoff doubles from `BaseDelay` (500ms) to `MaxDelay` (8s), with half of
each delay jittered. A `Retry-After` header is honored when the server
sends the delay-seconds form, capped at 30s so one header can't park the
run; the HTTP-date form is deliberately ignored rather than
half-supported. A cancelled context — a step or whole-test timeout —
stops the ladder immediately rather than blowing the budget that just
fired.

`-max-retries 1` disables retrying entirely, which is what you want when
debugging whether the endpoint is at fault.

### Timeouts multiply with retries

`-request-timeout` bounds one request; `-max-retries` decides how many are
sent. They compound: a request that always times out costs roughly
`timeout × attempts` before the run aborts, so raising one is a reason to
look at the other.

This is not theoretical. The default was 60s, and the first real-model run
against a large-context endpoint aborted on a step whose answer simply
took longer than that — then spent three minutes discovering it, because
the client-side timeout is classified as a transport error and retried.
The default is now 2m, and a genuinely slow model (CPU-only local
inference, a cold first request) may need more.

## Prior art this borrows from

- [Terminal-Bench](https://github.com/laude-institute/terminal-bench) —
  task structure (instruction + env + tests + oracle solution) and the
  observed small-model failure taxonomy (command/format errors dominate)
  that motivated this tool's "feed parse errors back to the model" retry
  behavior. Note the deliberate divergence on isolation: Terminal-Bench
  builds on Docker, while this tool uses OS-level confinement so it has
  no daemon to depend on (see "Sandboxing").
- [BFCL](https://gorilla.cs.berkeley.edu/leaderboard.html) / τ-bench —
  general multi-turn tool-calling evaluation shape; less directly
  applicable here since those score structured API calls, not an
  interactive terminal session, but worth knowing about if you later want
  to benchmark the SLM's tool-use ability in isolation from this harness.
