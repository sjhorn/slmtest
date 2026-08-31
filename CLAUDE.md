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
docs/model-runs.md        how to run against a real model, and what it has found
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
- **End with a step that checks ground truth, not the screen.** This is
  the strongest defence against a model asserting a pass it did not earn,
  and it is cheap. Running `tui-editor-test.md` against Qwen2.5-1.5B, the
  model claimed three passes in a row that were all false — it never typed
  the text, and its "save and quit" was two invalid keystrokes that vi
  answered with a bell. Every one of those verdicts was reached by reading
  a screen it had misunderstood. Step 5 ran `cat` against the filesystem,
  found nothing, and failed — which is the only reason the run reported
  FAIL rather than a clean sweep. A step whose Expect can be satisfied by
  the terminal's own echo, or by a TUI's redraw, is a step a weak model
  can talk itself past; one that reads state back out of the system cannot
  be faked.
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
- The prose contract above is the default, but `-native-tools` sends the
  same five actions as OpenAI `tools` instead and normalizes whatever
  comes back into the exact JSON this section describes, so nothing else
  changes. It is experimental and off by default: it measurably fixed
  the freeform-JSON failure modes for one model, but also measurably
  regressed that *same* model on a step's second turn. See
  `docs/model-runs.md`, "Using the OpenAI tools/tool_calls API", before
  turning it on.

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
| `-native-tools` | off | experimental: send actions as OpenAI `tools`/`tool_calls` instead of the prose JSON schema — off by default because it can regress a working model, not just fail to help; see `docs/model-runs.md` |
| `-temperature` | `0.1` | sampling temperature sent on every request, overriding the server's own default. There is no universally correct value — two models sharing byte-identical published `generation_config.json` settings measured as *opposite* recommendations for this harness's task shape; test both extremes per model rather than trusting either default. See `docs/model-runs.md` |
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
| `tui-editor-test.md` | anywhere with vi | six steps driving a full-screen TUI: modal input, a bare `i`, ESC as a control character, and `:wq` |
| `tui-claude-test.md` | anywhere with `claude` | drives Claude Code's own trust prompt — a real modern TUI — and exits without starting a session |
| `tui-claude-chat-test.md` | anywhere with `claude`, costs real API usage | trusts the folder, sends one real message, reads a real reply, exits via `/exit` |
| `nginx-smoke-test.md` | Linux with apt | aspirational — illustrates the format, does not run on macOS |

The two decline-only TUI specs are what exercise the PTY properly:
`send_keys` without Enter, control characters (`\u001b`, `\u0003`),
per-step `Size:`, and `term`. `tui-claude-test.md` is deliberately
scoped to the trust prompt — it never sends a message, so it costs no
tokens and stays deterministic, and it explicitly declines rather than
trusting the folder. Verified after a run: no project entry was created
and no session started. Both `tui-claude-test.md` and
`tui-claude-chat-test.md` create a fresh, uniquely-named directory via
`mktemp -d` rather than a fixed path — Claude Code records trust
decisions per path in `~/.claude.json` independent of whether the
directory still exists, so a fixed path can end up permanently marked
trusted by an earlier run and silently skip the trust prompt every later
run depends on. See `docs/model-runs.md`, "The one failing step was a
poisoned test fixture, not a model limit," for how this was found.

`tui-claude-chat-test.md` goes one step further and actually trusts the
folder, sends a real message, and reads a real reply before exiting via
`/exit` — unlike the decline-only spec, this costs real Claude API usage
and is genuinely non-deterministic. It also exists because getting it
working surfaced two real harness bugs (Enter sending the wrong byte for
a raw-mode TUI, and the schema having no way to express "press Enter
alone") and one open architectural gap (the consuming-diff design losing
on-screen content a model didn't act on immediately) — see
`docs/model-runs.md`, "Going further with Qwen3.5-9B," for the full
account, and the Known Gaps section below for the still-open one.

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

None of this touches a real model — see "CI runs no model" below, which
is a deliberate policy and not a gap.

The fake SLM deliberately fails the test if the runner asks for more
turns than its script provides — an unexpected extra model call is a bug
worth surfacing loudly rather than absorbing.

## CI runs no model. Model runs are local only.

**GitHub Actions never talks to an SLM or an LLM, and it must stay that
way.** CI covers the harness: unit tests, `go vet`, `gofmt`, and one
end-to-end smoke run against `examples/mock_slm_server.py`, which is
deterministic and needs no weights. A model run is not a pass/fail signal
about the code — the same spec on the same commit varies with the model,
its quantisation, and sampling — and weights don't belong in a CI cache.

**Consequence:** for a change to the runner, the action schema, or
`send_keys`/PTY handling, CI passing is not sufficient evidence. Run a
spec against a local model by hand first — see
[`docs/model-runs.md`](docs/model-runs.md) for the one-command setup and
what to run.

## Running against a real model, and what it has found

Everything above this line is verified against the deterministic mock.
Real-model testing — how to run one yourself, what has been found doing
so, and the sampling caveats on those findings — lives in
[`docs/model-runs.md`](docs/model-runs.md), updated as new runs happen so
this file stays a stable reference rather than a growing lab notebook.

The single most important thing in that file: **a model owns the
verdict, so a model willing to assert an unearned pass will produce
one** — observed more than once. Treat a summary line as a claim and the
`-json` transcript as the evidence.


## Known gaps / next steps for whoever extends this

- **The harness has no persistent "what's on screen" model — only a
  self-destructing diff of new bytes.** `SinceLastSnapshot()` in
  `internal/ptydriver/ptydriver.go` returns output written since the last
  call and resets the buffer. For an ordinary scrolling shell this is
  correct: each command's own output is exactly the new content worth
  showing, and a command that legitimately produces nothing should show
  nothing. But a raw-mode TUI can leave meaningful content sitting on
  screen indefinitely without re-emitting any bytes, and the model gets
  exactly one chance to notice it, in whatever turn it happens to arrive.
  Observed twice: a trust-menu's two-option text was gone by the next
  step (compounded by a since-fixed fixture bug, but the loss-of-content
  mechanism is independent of that fix), and a Claude Code chat reply
  that appeared once, alongside spinner-animation noise, was never shown
  again once the model didn't act on it that turn — see
  `docs/model-runs.md`, "Going further with Qwen3.5-9B," for both. A
  naive fix (fall back to the last non-empty output when nothing new
  arrived) was considered and rejected: it would fix the TUI case but
  actively mislead the model in the far more common case of a command
  that legitimately produces no output, by re-showing stale content as a
  fresh result. The real fix is a proper terminal-screen model — tracking
  cursor position and a persistent grid of visible cells via ANSI/VT
  interpretation — rather than a raw byte diff, which is a substantial
  rewrite of `ptydriver`'s core model, not a patch. Whoever picks this up
  should start by deciding whether that screen model lives in
  `ptydriver` (returning a rendered screen alongside the raw diff) or is
  a separate layer the runner consults only when a turn's diff is empty.
- **A model can assert a pass it did not earn.** The harness cannot close
  this without taking over the judgement it exists to delegate. Treat a
  summary line as a claim and the `-json` transcript as the evidence — see
  [`docs/model-runs.md`](docs/model-runs.md) for observed cases.
- **`notExecutedNote` describes the past but not its consequence for the
  next turn.** When a `send_keys` doesn't press Enter, the note correctly
  says the text hasn't run — but doesn't warn that it's still sitting in
  the terminal's input buffer, waiting to concatenate with whatever the
  model types next. A model that follows the note's own advice
  (`run_command` to execute it) without clearing that stale input first
  produces a garbled, self-inflicted command it has no way to recognize
  as self-inflicted. Observed live — see `docs/model-runs.md`, the xLAM
  `tui-claude-test.md` deep dive. Not model-specific: any model that acts
  on the note as literally worded hits this. Fix belongs in
  `notExecutedNote` in `internal/runner/runner.go` — either warn about
  the stranded input directly, or have the harness send a clearing
  keystroke (e.g. Ctrl-U) before the next action runs.
- **Sandboxing is macOS-only, deliberately, for now.** `-sandbox` is
  Seatbelt, which is macOS-specific; `-sandbox` errors on Linux with a
  message pointing at `-exec-prefix` instead. This was a scoping choice
  when Seatbelt was the whole point of the feature (see "Sandboxing"
  below for why it was chosen over a container runtime), not an oversight
  — but it's a real gap and closing it is planned. A Landlock or
  bubblewrap backend behind the existing `sandbox.Config` interface is the
  intended shape: same `-sandbox`/`-sandbox-write`/`-sandbox-deny-network`
  flags, a different profile generator underneath. Whoever picks this up
  should start in `internal/sandbox/sandbox.go`.
- **Sandboxing confines writes only, even on macOS.** The profile is a
  deny-list over a shared filesystem: reads are unrestricted, and it is
  not a boundary against hostile code.
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
