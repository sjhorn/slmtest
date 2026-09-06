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
alternative). First check nothing is already listening on the port
you're about to use — a stale server from an earlier session is an easy
way to get a confusing "Address already in use" error, or (worse) to
silently talk to the wrong model:

```
curl -s http://localhost:8084/v1/models
```

If that returns a model list, something's already serving — either reuse
it and skip straight to the `slmtest run` command below, or pick a
different `--port`/`-endpoint` for a fresh instance. Otherwise:

```
uv venv --python 3.12 .venv && uv pip install --python .venv/bin/python mlx-lm
.venv/bin/mlx_lm.server --model mlx-community/Qwen3.5-9B-8bit \
  --chat-template-args '{"enable_thinking":false}' --prompt-cache-size 8 --port 8084
```

(`.venv/bin/mlx_lm.server`, not `.venv/bin/python -m mlx_lm.server` —
the latter still works but is deprecated and prints a warning on every
start.)

Then, in another terminal:

```
./slmtest run examples/echo-test.md -endpoint http://localhost:8084/v1 \
  -model mlx-community/Qwen3.5-9B-8bit -request-timeout 3m
```

Every remaining step reuses this `-endpoint`/`-model`/`-request-timeout`
trio — abbreviated as `"${SLM[@]}"` below. Set it as an **array**, not a
plain string: in zsh (the macOS default), an unquoted plain-string
variable does *not* word-split into separate arguments the way it does
in bash, so `SLM="-endpoint ..."` followed by `slmtest run ... $SLM`
fails with `flag provided but not defined` — the whole string gets
passed as one argument. An array avoids that in both shells:

```
SLM=(-endpoint http://localhost:8084/v1 -model mlx-community/Qwen3.5-9B-8bit -request-timeout 3m)
```

(This only lasts for the current shell session — set it again if you
open a new terminal.)

## 3. Drive a real TUI: persistent screen model + modifier keys

```
./slmtest run examples/nano-edit-test.md "${SLM[@]}" -continue-on-fail
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
./slmtest-browser run examples/browser-mouse-test.md "${SLM[@]}" \
  -driver-option url=file://$PWD/examples/browser-mouse.html
```

Exercises `double_click`, `right_click`, and `drag` against a real local
page. For a richer, multi-behavior script in the same style, try:

```
./slmtest-browser run examples/task-board-test.md "${SLM[@]}" \
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
./slmtest-browser run examples/login-flow-test.md "${SLM[@]}" \
  -driver-option url=file://$PWD/examples/login-flow.html
```

**Level 2 — a Feature file: `## Background` + tagged `## Scenario:`
sections, each scenario getting its own independent browser session:**

```
./slmtest-browser run examples/login-flow-feature-test.md "${SLM[@]}" \
  -driver-option url=file://$PWD/examples/login-flow.html -continue-on-fail
```

Then try tag-based selection — only the `@smoke`-tagged scenario runs:

```
./slmtest-browser run examples/login-flow-feature-test.md "${SLM[@]}" \
  -driver-option url=file://$PWD/examples/login-flow.html -tag @smoke
```

**Level 3 — `## Scenario Outline:` + `### Examples` data table, expanded
into one independent scenario per row:**

```
./slmtest-browser run examples/login-validation-outline-test.md "${SLM[@]}" \
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
./slmtest-browser run examples/cucumber-sample-login-test.md "${SLM[@]}" \
  -driver-option url=file://$PWD/examples/login-flow.html -continue-on-fail
```

The second targets the *real public site it was written for* —
`saucedemo.com` — not a local fixture:

```
./slmtest-browser run examples/cucumber-sample-checkout-split-test.md "${SLM[@]}" \
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

## 7a. Agent-authoring a spec: explore, draft, validate, run

An agent with shell access (Claude Code, say) doesn't need a human to
hand it a finished `.md` spec — it can build one directly, the same way
a QA engineer would:

1. **Explore the live target directly**, outside slmtest entirely — run
   the actual commands in a shell, or open the actual page in a browser,
   to learn what the real Goal/Hint/Expect for each step should be. This
   is ordinary shell/browser use, not an slmtest call.
2. **Draft a `.md` spec** from what was learned — `slmtest init
   draft-test.md` scaffolds the frontmatter + one-step template to start
   from (see §9 below), or write the file directly if the shape is
   already clear.
3. **`validate_test` after every edit.** It's parse-only — no model call,
   no PTY, no browser — so it's cheap enough to call after every single
   change while a spec is still being drafted:
   ```
   slmtest validate draft-test.md
   ```
   or, over MCP, `validate_test` with `spec_path` set. A parse error
   points at exactly what's malformed (a missing `Expect:`, bad
   frontmatter) before any real run is attempted.
4. **`run_test`/`slmtest run` once the spec looks complete.** If a step
   fails, the report says which one and why (see "Reviewing what
   happened," next) — fix that step and validate/run again. This loop
   (explore → draft → validate → run → fix) is exactly how a human
   iterates on a spec by hand; nothing about it requires new tooling.

## 7b. Reviewing what happened: the audit trail

Once a run finishes — especially one that failed, or passed in a way
worth double-checking (see CLAUDE.md's "a model can assert a pass it did
not earn") — the `-json` report and the artifacts below are how an agent
(or a human) reconstructs exactly what the model saw and decided,
without re-running anything:

- **`run_context`** in the `-json` report names the endpoint, model,
  driver, temperature, and `slmtest`/git version the run actually used —
  "what was this run testing against."
- **Per-turn `screen`** in each step's `transcript` is the driver's full,
  untruncated screen/DOM snapshot at that turn — not just the diff text
  the model was shown, but the complete state at that moment.
- **`started_at`/`finished_at`/`duration_ms`** at the report, step, and
  turn level pin down exactly when each decision happened.
- **`-trace <dir>`** persists every turn's screen snapshot as its own
  file under a per-test directory, plus a `manifest.json` tying
  step → turn → snapshot file together and a full `report.json` — a
  self-contained bundle an agent can read back file-by-file instead of
  parsing one large JSON blob.

None of this is a live-drive/record feature — there's no way to *steer*
a run this way, only to inspect one after the fact. See CLAUDE.md's
"Known gaps" for why that scope was deliberate. See §7c below, "Handoff
recipe," for the concrete commands to capture this for someone else
(human or agent) to review afterward.

## 7c. CI and regression artifacts

```
slmtest run examples/echo-test.md "${SLM[@]}" \
  -junit out.junit.xml -trace ./trace -golden ./golden
```

- **`-junit <path>`** writes a standard JUnit XML document (one
  `<testsuite>` per Test/Scenario, one `<testcase>` per step) — every
  major CI system already turns this into inline PR annotations for
  free.
- **`-trace <dir>`** writes the replayable bundle described above.
- **`-golden <dir>`** compares each step's final screen against a
  checked-in baseline, reporting `match`/`mismatch`/`missing` per step
  on stderr; **`-golden-update`** writes/overwrites the baselines
  instead of comparing. Golden results never affect a step's pass/fail
  or the process exit code — they're a complement to the model's own
  verdict, not a replacement for it. First run against a fresh directory
  reports `missing` for every step (nothing to compare against yet); run
  with `-golden-update` once to create baselines, then plain `-golden`
  on later runs to check for drift.

All three flags have `run_test` equivalents over MCP (`junit_path`,
`trace_dir`, `golden_dir`, `golden_update`).

**`-trace`/`-junit` overwrite on every run; they don't append or
version.** If you're handing a run off to someone/something else to
execute (a teammate, a CI job, another agent) and want the artifacts
kept around for a later review, point them at a **fresh directory per
run** rather than reusing one path — see CLAUDE.md's "CI/audit-trail
artifacts" for exactly why.

### Handoff recipe: run with full trace logging, then hand off for review

The concrete version of "run it, then have someone else check the
transcript" — whether "someone else" is a teammate, a CI job, or another
agent instance:

**CLI:**
```
run_dir="./slm-runs/$(date +%Y%m%d-%H%M%S)-my-test"
mkdir -p "$run_dir"
./slmtest run my-test.md "${SLM[@]}" \
  -trace "$run_dir/trace" -junit "$run_dir/report.junit.xml"
```
Then hand over `$run_dir` (or just `$run_dir/trace`) — it's self-
contained: `manifest.json` plus `report.json` plus every turn's full
screen snapshot, no need to re-run anything to inspect what happened.

**MCP (`run_test`):**
```json
{
  "spec_path": "my-test.md",
  "endpoint": "http://localhost:8080/v1",
  "trace_dir": "./slm-runs/20260906-153000-my-test/trace",
  "junit_path": "./slm-runs/20260906-153000-my-test/report.junit.xml"
}
```
Same output on disk either way, since both surfaces call the same
`internal/cliops` code — pick whichever a given caller finds easier to
drive. Either way, once the run finishes, point a reviewer (human or
agent) at the resulting directory: `manifest.json` is the index, each
`step-N-turn-M.txt` is a full screen snapshot, and `report.json` has the
complete report (timestamps, `run_context`, every turn's action/reason)
for cross-referencing a specific step or turn without needing the
original terminal output.

## 8. Sandboxing (macOS only)

```
./slmtest run examples/workspace-test.md "${SLM[@]}" -sandbox -continue-on-fail
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
iterating, and `./slmtest run my-test.md "${SLM[@]}"` when ready.

## Reference

- [`CLAUDE.md`](CLAUDE.md) — the full spec format, the JSON action
  contract, driver abstraction, and every known gap.
- [`docs/model-runs.md`](docs/model-runs.md) — every real-model finding
  in this project's history, in order, with the evidence.
