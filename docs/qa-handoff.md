# QA handoff: run slmtest via MCP, leave an audit trail for review

Paste a pointer to this file (or its contents) as the first message into
a Claude Code session in **any** project directory to have it drive a
real QA run against `slmtest`'s MCP server, then hand the result back
for review by someone/something else — a human, or another Claude
session with fresh eyes on the trace bundle.

Fill in `SPEC_PATH` and `ENDPOINT` below before using this.

## 1. Register the MCP server (skip if already connected)

```
claude mcp list
```

If `slmtest` isn't listed as connected, register it — the path is
absolute, so this works regardless of which project directory you're
running Claude Code from:

```
claude mcp add slmtest /Users/shorn/dev/go/slmtest/slmtest-mcp -s local
```

Reconnect/restart if needed so the `run_test`/`validate_test`/`init_test`
tools show up.

## 2. Run the test with a full audit trail

Call the `run_test` tool (from the `slmtest` MCP server) with these
params — fill in `SPEC_PATH` (a slmtest `.md` spec file) and `ENDPOINT`
(an OpenAI-compatible SLM endpoint, e.g. `http://localhost:8080/v1`):

```json
{
  "spec_path": "SPEC_PATH",
  "endpoint": "ENDPOINT",
  "trace_dir": "/Users/shorn/dev/go/slmtest/slm-runs/<timestamp>-<short-name>/trace",
  "junit_path": "/Users/shorn/dev/go/slmtest/slm-runs/<timestamp>-<short-name>/report.junit.xml"
}
```

Use a fresh `<timestamp>-<short-name>` directory per run — these paths
overwrite on reuse, they don't append or version (see CLAUDE.md's
"CI/audit-trail artifacts" for why).

The absolute path back into `/Users/shorn/dev/go/slmtest/slm-runs/...` is
deliberate: it's what lets a reviewer on that machine read the results
afterward without needing filesystem access to wherever this session's
own project directory happens to be.

## 3. Report back — don't interpret, don't fix

Report back only:
- whether the run passed
- the exact `trace_dir` path used

**Do not fix, retry, or reinterpret a failing step.** The point of this
run is an unaltered result for someone else to review — quietly patching
a failure before it's ever reviewed defeats the purpose of the handoff.
Don't summarize the transcript either; the trace bundle (`manifest.json`
+ `report.json` + one screen snapshot per turn) is the artifact itself.

## What the reviewer does next

Once a path comes back (e.g. `slm-runs/20260906-153000-checkout/`), a
reviewer reads it directly — no MCP access needed, no re-running
anything:

- `manifest.json` — the index: step → turn → snapshot file, with
  action/status/reason/timestamp per turn
- `report.json` — the full report: timestamps, `run_context`
  (endpoint/model/driver/temperature/version), every turn's transcript
- `step-N-turn-M.txt` — the exact, untruncated screen the model saw at
  that turn

This is enough to tell a real harness/spec bug apart from the model
simply asserting a pass it didn't earn (see CLAUDE.md's "Known gaps" —
"a model can assert a pass it did not earn").
