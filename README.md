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

Go 1.22+. One dependency (`github.com/creack/pty`).

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

A working scaffold, not a finished product. Notably: it has been exercised
against a large model but not yet against a genuinely small one, which is
the case several design decisions were made for. See the "Known gaps"
section of `CLAUDE.md`.
