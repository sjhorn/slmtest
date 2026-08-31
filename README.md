# slmtest

Run interactive terminal tests against a language model, where the test is
a plain markdown file.

`slmtest` spawns a real shell in a pseudo-terminal and hands a model one
step at a time: here's the goal, here's a suggested command, here's what
"passed" means. The model runs commands, reads the output, and returns a
verdict for each step. It's the way a human QA engineer works through a
manual test script — read the step, try something, look at the screen,
decide, move on.

```markdown
---
name: workspace-test
shell: /bin/bash
---

## Step 1: Create a scratch workspace
Goal: an empty directory exists at /tmp/slmtest-workspace.
Hint: mkdir -p /tmp/slmtest-workspace
Expect: `ls -d /tmp/slmtest-workspace` prints the path without an error.

## Step 2: Write a file into the workspace
Goal: notes.txt contains the line "alpha beta gamma".
Hint: echo 'alpha beta gamma' > /tmp/slmtest-workspace/notes.txt
Expect: `cat /tmp/slmtest-workspace/notes.txt` prints "alpha beta gamma".
```

```
$ slmtest run examples/workspace-test.md -endpoint http://localhost:8080/v1
Test: workspace-test
  [PASS] step 1: Create a scratch workspace (2 turns) — directory exists
  [PASS] step 2: Write a file into the workspace (2 turns) — file contains the line
RESULT: PASS
```

## Why this shape

**The markdown is the spec.** The goal, the suggested command, and the
success criterion live together in one file that a human and a model can
both read. There's no separate test script in another language.

**A hint is a suggestion, not a script.** If the suggested command fails,
the model is expected to reason about why — missing package, wrong path,
service still starting — and try something else. That latitude is the
point; a test that only ever runs one fixed command doesn't need a model.

**Every step gets its own verdict.** The report says *which* step failed
and why, rather than reducing a whole run to one bit.

## Install

```
go build -o slmtest ./cmd/slmtest
```

Go 1.25+ (the floor moved from 1.22 when the MCP server's official SDK
dependency was added — see [`CLAUDE.md`](CLAUDE.md)'s "MCP server"
section). One dependency for the default build (`github.com/creack/pty`);
a browser driver and an MCP server are available too, each opt-in — see
below.

## Use

```
slmtest run <file.md> [flags]     # execute a spec
slmtest validate <file.md>        # parse-check only, no model call
slmtest init <file.md>            # write a starter template
```

Flags go **after** the file path. Exit code is 0 if every step passed, 1
otherwise — usable directly in CI, as is `-json`.

Point `-endpoint` at anything OpenAI-compatible — llama.cpp's
`llama-server`, vLLM, LM Studio, Ollama's compat mode, or a hosted API:

```
slmtest run t.md -endpoint http://localhost:8080/v1 -model my-model
```

### Running a small model locally

`llama-server` (from llama.cpp) is the lightest way to do this — one
binary, no daemon, and it fetches the weights itself:

```
llama-server -hf Qwen/Qwen2.5-1.5B-Instruct-GGUF:Q4_K_M --port 8080 -c 8192
slmtest run examples/workspace-test.md -endpoint http://localhost:8080/v1
```

`slmtest` deliberately has no llama.cpp bindings. It speaks
OpenAI-compatible HTTP and so does `llama-server`, so there is nothing for
a binding to bridge — and adding one would drag a C++ toolchain into a
build that is currently pure Go with a single dependency.

Small models are slow on CPU, so raise the per-request budget:
`-request-timeout 5m`.

| Flag | Does |
|---|---|
| `-endpoint`, `-model`, `-api-key` | where to send requests |
| `-json` | machine-readable report instead of the human one |
| `-verbose` | stream each turn to stderr as it happens |
| `-continue-on-fail` | attempt every step instead of stopping at the first failure |
| `-sandbox` | confine the shell with macOS Seatbelt |
| `-step-timeout`, `-max-retries` | budgets |

`slmtest run -h` lists them all.

## Sandboxing

`-sandbox` confines the shell using macOS Seatbelt: writes are limited to
`/tmp`, `/var/tmp` and `$TMPDIR` (add more with `-sandbox-write`), and
`-sandbox-deny-network` blocks the network. Reads are unrestricted.

There's no container runtime involved — nothing to install, no daemon to
keep running, no image to pull. The trade is that this *confines* the host
filesystem rather than replacing it, so it stops a test from scribbling
over your home directory but is **not** a boundary against hostile code.
For stronger isolation, or on Linux, `-exec-prefix` wraps the shell in
whatever you like:

```
slmtest run t.md -exec-prefix "docker run --rm -it ubuntu:24.04" -shell /bin/sh
slmtest run t.md -exec-prefix "ssh testbox"
```

## Driving a TUI

The PTY is real, so a spec can drive a full-screen terminal UI:

```
slmtest run examples/tui-editor-test.md -endpoint ...          # vi: insert mode, ESC, :wq
slmtest run examples/tui-claude-test.md -endpoint ...           # Claude Code's trust prompt, declines, costs nothing
slmtest run examples/tui-claude-chat-test.md -endpoint ...      # trusts the folder, one real message, real reply
slmtest run examples/tui-claude-advanced-test.md -endpoint ...  # a real multi-file coding task, verified on disk
```

These use `send_keys` (which types without pressing Enter), control
characters like `\u001b` for ESC, and a per-step `Size:` so the TUI gets
the geometry it expects. The last two specs cost real API usage and take
real wall-clock time (the advanced one runs an actual multi-step coding
task) -- the first `tui-claude-*` spec stays free and deterministic by
design; reach for the others deliberately.

## Pluggable drivers

The turn loop, spec format, and model prompting don't know or care what
UI surface they're driving — that's `internal/driver.Driver`. The
terminal (`tui`) is the default and needs nothing extra. A `browser`
driver (real Chromium via Playwright-Go) is also available, opt-in via a
build tag so it doesn't add a dependency to the default binary:

```
go build -tags browserdriver -o slmtest ./cmd/slmtest
slmtest run examples/browser-test.md -driver-option url=file:///path/to/page.html
```

Select a driver per spec with `driver: browser` in frontmatter, or
per-run with `-driver browser`. See [`CLAUDE.md`](CLAUDE.md)'s "Driver
abstraction" section for the full design (shared interaction primitives,
how a spec's `driver_options` map to CLI flags, etc).

## MCP server

`slmtest-mcp` exposes `run_test`/`validate_test`/`init_test` as MCP tools
over stdio, for an agent (Claude Code, say) that wants a structured,
typed interface instead of shelling out to the CLI and parsing `-json`:

```
go build -o slmtest-mcp ./cmd/slmtest-mcp
```

Point an MCP client's config at the resulting binary. See
[`CLAUDE.md`](CLAUDE.md)'s "MCP server" section for the tool params and
what's verified.

## Trying it without a model

`examples/mock_slm_server.py` is a deterministic stand-in that exercises
the harness's own plumbing:

```
python3 examples/mock_slm_server.py &
slmtest run examples/echo-test.md -endpoint http://localhost:8080/v1 -verbose
```

## Development

```
go test ./...          # ~15s, mostly PTY wait time
go test -race ./...
```

[`CLAUDE.md`](CLAUDE.md) is the deep documentation: the full spec format,
the JSON action contract the model must follow, why the parser and runner
are shaped the way they are, and the known gaps. Read it before changing
anything in `internal/`.

## Status

A working scaffold, not a finished product.

It has been exercised against a large hosted model and a growing roster
of small local ones via llama.cpp — Qwen2.5 at 0.5B/1.5B, xLAM-2-1b-fc-r,
Qwen2.5-Coder-7B-Instruct, and Qwen3.5-9B. The small models found real
defects the mock and the large model never surfaced, and rerunning
findings against later models has more than once turned up bugs in the
harness itself, not just the model under test — including two found
while pushing a real coding task through Claude Code's own TUI. The
current best result: Qwen3.5-9B clears every hard spec in this project,
including a real multi-file agentic task verified against the
filesystem, not the screen. See
[`docs/model-runs.md`](docs/model-runs.md), which is the most useful page
in the repo if you plan to change the runner, and doubles as the runbook
for testing against a model yourself.

One limitation is worth knowing before you trust a green result: because
the model owns the verdict, a model willing to assert an unearned pass
will produce one, and that has been observed more than once. The `-json`
report carries the full transcript so you can check. See "Known gaps" in
`CLAUDE.md`.
