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
internal/runner/          the per-step turn loop that ties it all together
examples/                 sample test specs + a mock SLM server for smoke tests
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
  that never started, a file that was never created). If you want every
  step attempted regardless of earlier failures, that's a `runner.Run`
  behavior change (see "Continue-on-fail" below), not a spec-file setting.

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

**Flags go after the file path**, matching the documented usage
(`slmtest run <file.md> [flags]`) — this is enforced explicitly in
`main.go`'s `takeLeadingPositional`, because Go's stdlib `flag` package
stops parsing at the first non-flag token and will otherwise silently
swallow flags placed after a positional argument.

Exit code is `0` if every step passed, `1` otherwise (including aborts) —
safe to use directly in CI.

Other commands:

```
slmtest validate <file.md>   # parse-check only, no execution, no model call
slmtest init <file.md>       # write a starter template to file.md
```

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

## Known gaps / next steps for whoever extends this

- **No sandboxing.** The PTY driver launches a real shell process
  directly — there's no container/chroot/jail around it. For anything
  beyond local dev smoke tests, run this inside Docker/Apptainer/a VM
  yourself, the way Terminal-Bench does. This is a scaffold, not a secure
  sandbox.
- **Continue-on-fail** isn't wired up yet — `runner.Run` currently
  `break`s on the first non-passing step (see the comment at that `break`
  in `internal/runner/runner.go`). Flip it to `continue` (and stop
  clobbering `report.Passed` logic accordingly) if you want every step
  attempted regardless of earlier failures.
- **No terminal-size-sensitive step handling.** `pty.Setsize` is called
  once at start (`40x200`). Fine for most CLI output; may need per-step
  resizing if a test drives something like `vim` or a TUI that reflows.
- **History is per-step, not per-test.** Each step starts the model's
  chat history fresh (only the system prompt persists). This is
  intentional — it keeps context small and stops step N's failed
  attempts from polluting step N+1's reasoning — but it also means the
  model can't reference "what I did two steps ago" if a later step
  genuinely depends on it. If you need that, thread a short rolling
  summary of prior step outcomes into each step's first user message.
- **Retry/backoff on transport errors** isn't implemented — an HTTP
  error against the SLM endpoint currently aborts the whole run
  immediately (`internal/runner/runner.go`, the `client.Complete` error
  branch). Add retry-with-backoff there if your endpoint is flaky.

## Prior art this borrows from

- [Terminal-Bench](https://github.com/laude-institute/terminal-bench) —
  task structure (instruction + env + tests + oracle solution) and the
  observed small-model failure taxonomy (command/format errors dominate)
  that motivated this tool's "feed parse errors back to the model" retry
  behavior.
- [BFCL](https://gorilla.cs.berkeley.edu/leaderboard.html) / τ-bench —
  general multi-turn tool-calling evaluation shape; less directly
  applicable here since those score structured API calls, not an
  interactive terminal session, but worth knowing about if you later want
  to benchmark the SLM's tool-use ability in isolation from this harness.
