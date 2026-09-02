# Usage walkthrough

A set of concrete steps to try `slmtest` yourself, roughly in the order
this project's own capabilities were built and verified. Each step names
what it demonstrates, the exact command(s) to run, and what to expect.
[`README.md`](README.md) is the pitch; [`CLAUDE.md`](CLAUDE.md) is the
full reference; this is the "just try it" path.

Every command below assumes you're in the repo root.

## 0. Build

```
go build -o slmtest ./cmd/slmtest
```

That's the default binary — the `tui` driver only, no browser, no MCP.
Two more binaries are opt-in and built separately (steps 5 and 9 below).

## 1. Smoke-test the harness with no model at all

Confirms the plumbing works before you touch a real model.

```
python3 examples/mock_slm_server.py &
./slmtest run examples/echo-test.md -endpoint http://localhost:8080/v1 -verbose
```

Expect one `[PASS]` line. `-verbose` streams each turn to stderr so you
can see the prompt/reply/PTY-output cycle happen. Kill the mock server
(`kill %1` or `pkill -f mock_slm_server.py`) when done.

## 2. Point it at a real local model

On Apple Silicon, `mlx-lm` is the recommended setup (see `README.md`'s
"Running a small model locally" for the full rationale and `llama.cpp`
alternative):

```
uv venv --python 3.12 .venv && uv pip install --python .venv/bin/python mlx-lm
.venv/bin/python -m mlx_lm.server --model mlx-community/Qwen3.5-9B-8bit \
  --chat-template-args '{"enable_thinking":false}' --prompt-cache-size 8 --port 8084
```

Then, in another terminal:

```
./slmtest run examples/echo-test.md -endpoint http://localhost:8084/v1 \
  -model mlx-community/Qwen3.5-9B-8bit -request-timeout 3m
```

Every remaining step reuses this `-endpoint`/`-model`/`-request-timeout`
trio — abbreviated as `$SLM` below:

```
SLM="-endpoint http://localhost:8084/v1 -model mlx-community/Qwen3.5-9B-8bit -request-timeout 3m"
```

## 3. Drive a real TUI: persistent screen model + modifier keys

```
./slmtest run examples/nano-edit-test.md $SLM -continue-on-fail
```

Watch for: nano's status-bar UI (not vi's modal one), a cut/paste
round-trip using `press_key` with `modifiers: ["ctrl"]` (Ctrl+K/Ctrl+U/
Ctrl+W/Ctrl+O/Ctrl+X — real chords, not raw control bytes), and a final
`cat` of the saved file as ground truth, not a screen read. Add
`-verbose` to watch the "Current screen contents" block in each
turn's observation — that's the persistent VT100-emulator-backed screen
model (`internal/ptydriver/screen.go`), not the consuming byte-diff.

## 4. Drive a real browser: mouse and keyboard primitives

Needs Chromium once: `go run github.com/mxschmitt/playwright-go/cmd/playwright install chromium`

```
go build -tags browserdriver -o slmtest-browser ./cmd/slmtest
./slmtest-browser run examples/browser-mouse-test.md $SLM \
  -driver-option url=file://$PWD/examples/browser-mouse.html
```

Exercises `double_click`, `right_click`, and `drag` against a real local
page. For a richer, multi-behavior script in the same style, try:

```
./slmtest-browser run examples/task-board-test.md $SLM \
  -driver-option url=file://$PWD/examples/task-board.html -continue-on-fail
```

Typed input, a keyboard-only interaction with no click at all, a drag
between two drop targets, keyboard deletion, and a final check via a DOM
counter the actions never touch directly.

## 5. BDD/Gherkin-style specs: does the markdown stretch that far?

Three levels, each a real `.md` file — no new binary needed, same
`slmtest-browser` from step 4.

**Level 1 — Given/When/Then phrasing, the plain flat format, zero code
changes:**

```
./slmtest-browser run examples/login-flow-test.md $SLM \
  -driver-option url=file://$PWD/examples/login-flow.html
```

**Level 2 — a Feature file: `## Background` + tagged `## Scenario:`
sections, each scenario getting its own independent browser session:**

```
./slmtest-browser run examples/login-flow-feature-test.md $SLM \
  -driver-option url=file://$PWD/examples/login-flow.html -continue-on-fail
```

Then try tag-based selection — only the `@smoke`-tagged scenario runs:

```
./slmtest-browser run examples/login-flow-feature-test.md $SLM \
  -driver-option url=file://$PWD/examples/login-flow.html -tag @smoke
```

**Level 3 — `## Scenario Outline:` + `### Examples` data table, expanded
into one independent scenario per row:**

```
./slmtest-browser run examples/login-validation-outline-test.md $SLM \
  -driver-option url=file://$PWD/examples/login-flow.html -continue-on-fail
```

`validate` shows the expansion without running anything — useful while
authoring:

```
./slmtest validate examples/login-flow-feature-test.md
./slmtest validate examples/login-validation-outline-test.md
```

See `CLAUDE.md`'s "BDD/Gherkin-style Feature files" section for the full
format reference, and `docs/model-runs.md`'s "The BDD-format
investigation" for how far this was pushed and why no second parser
turned out to be needed.

## 6. Real Cucumber `.feature` files this project didn't author

Two real, external `.feature` files (found via GitHub code search, not
picked to be easy), translated into this markdown dialect:

```
./slmtest-browser run examples/cucumber-sample-login-test.md $SLM \
  -driver-option url=file://$PWD/examples/login-flow.html -continue-on-fail
```

The second targets the *real public site it was written for* —
`saucedemo.com` — not a local fixture:

```
./slmtest-browser run examples/cucumber-sample-checkout-split-test.md $SLM \
  -driver-option url=https://www.saucedemo.com/ -continue-on-fail
```

(`cucumber-sample-checkout-test.md`, without `-split`, is the original,
more literal translation — it reliably fails one assertion for a
documented, real reason; see `docs/model-runs.md` for the finding and
the fix the `-split` version demonstrates. Both are worth running to see
the difference.)

## 7. The MCP server

For an agent (Claude Code, say) that wants a typed tool interface instead
of shelling out to the CLI:

```
go build -o slmtest-mcp ./cmd/slmtest-mcp
```

Point an MCP client's config at the resulting binary (stdio transport).
`run_test`/`validate_test` auto-detect a Feature-style spec the same way
the CLI does — a `tags` param on `run_test` mirrors `-tag`. See
`CLAUDE.md`'s "MCP server" section for the full tool params.

## 8. Sandboxing (macOS only)

```
./slmtest run examples/workspace-test.md $SLM -sandbox -continue-on-fail
```

Confines the shell's writes to scratch directories (`/tmp`, `/var/tmp`,
`$TMPDIR`) via Seatbelt — no daemon, no container. `workspace-test.md`'s
step 4 is written to pass either way and report which happened, so this
one run shows you the difference `-sandbox` makes.

## 9. Author your own spec

```
./slmtest init my-test.md
```

Writes a starter template with the `Goal`/`Hint`/`Expect` shape. Edit it,
then `./slmtest validate my-test.md` (fast, no model call) while
iterating, and `./slmtest run my-test.md $SLM` when ready.

## Reference

- [`CLAUDE.md`](CLAUDE.md) — the full spec format, the JSON action
  contract, driver abstraction, and every known gap.
- [`docs/model-runs.md`](docs/model-runs.md) — every real-model finding
  in this project's history, in order, with the evidence.
