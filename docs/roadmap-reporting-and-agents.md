# Roadmap: audit-trail reporting + agent-authoring for slmtest

**Status: Phases A–D implemented; Phase E scoped down to documentation
only (no "record mode") — see the end of each phase's section below and
CLAUDE.md's "CI/audit-trail artifacts" section for the shipped design.**

This started as a research/roadmap document, not an implementation plan
for a single feature. It exists to answer two questions the project had
not addressed yet:

1. Can a `slmtest` run's output become a genuine CI/build-pipeline
   artifact with an audit trail, the way established test frameworks
   (JUnit, pytest, Playwright, Allure, Flutter's test tooling) capture
   and report results — rather than today's one JSON blob with no
   timestamps, no environment metadata, and no persisted screenshots?
2. Can `slmtest` become a tool a coding-agent harness (Claude Code,
   Codex, etc.) reaches for directly — both to *build* a test suite by
   exploring a TUI/UI and authoring `.md` specs, and to *run* one to
   confirm a screen/UI is behaving as expected?

Nothing here is implemented yet. It is a comparison against prior art,
plus a phased roadmap to pick from once reviewed. See CLAUDE.md for the
current architecture and the "Known gaps" section this doc extends.

## What slmtest captures today

Confirmed by direct inspection of `internal/runner/runner.go` and the
two drivers:

- **`Report{Test, Steps, Passed, Aborted}`**, **`StepOutcome{Step,
  Result, Reason, Turns, Transcript, TimedOut, Aborted}`**,
  **`TurnLog{UserPrompt, RawReply, Action, PTYOutput, Err}`**.
  `Report.MarshalJSON` is the documented `-json` CI contract, and it's
  reused byte-for-byte by `cmd/slmtest-mcp`'s `run_test`
  `StructuredContent` and by Feature mode's per-scenario wrapping.
- **Never captured anywhere:** run/step/turn timestamps, run duration,
  which model/endpoint/temperature/driver produced a report, a git
  commit or `slmtest` version stamp, pixel screenshots, HTML/DOM dumps,
  golden-baseline comparisons, or any CI-standard result format (JUnit
  XML, TAP, SARIF, ...).
- **Captured but only as an opaque text blob, not a dedicated field:**
  the terminal screen (`internal/ptydriver/screen.go`'s VT100 model) and
  the browser accessibility-tree snapshot (`internal/browserdriver`) are
  both folded into whichever turn's `pty_output` string produced them —
  ephemeral beyond that. Nothing is ever written to disk as a distinct
  artifact; there is no `os.WriteFile`/screenshot call anywhere in
  either driver.
- CLAUDE.md's "Known gaps" section already comes closest to naming this:
  *"A model can assert a pass it did not earn... treat a summary line as
  a claim and the `-json` transcript as the evidence."* — but it stops
  there. There is no existing discussion of timestamps, artifacts, or
  CI-standard export formats anywhere in the repo.

## Prior art surveyed

### Flutter ecosystem

| Tool | Report format | Notes |
|---|---|---|
| [`flutter_test` golden tests](https://api.flutter.dev/flutter/flutter_test/matchesGoldenFile.html) | none — PNGs checked into the repo, compared pixel-for-pixel; normal test exit code only | Platform-rendering drift is handled by generating a *second, deterministic* golden set with a fixed font — the [Alchemist](https://pub.dev/packages/alchemist) package's `goldens/` vs `goldens/ci/` split — rather than a tolerance built into core Flutter (an open, unresolved feature request) |
| [`integration_test`](https://docs.flutter.dev/testing/integration-tests) | Dart `test`'s line-delimited JSON event stream (no native JUnit XML — the community's `junitreport` package converts it) | Screenshots/video are bolted on via `flutter drive`, not unified with the JSON report. [flutter/flutter#90937](https://github.com/flutter/flutter/issues/90937) — "one command, JSON report + screenshot-on-failure" — has been open for years, still unresolved |
| [Patrol](https://patrol.leancode.co/) | none of its own | Adds native automation (permission dialogs, etc.) on top of `integration_test`; inherits the same JSON/JUnit story |
| `ai_flutter_agent` (closest direct analog to slmtest found) | a homegrown `AuditLog` of every action attempt plus structured accessibility-tree diffs | Drives the UI via the Semantics tree in a Perceive→Plan→Execute→Verify loop. Validates that an LLM-driven UI-testing tool needs its own audit-log concept, since nothing upstream (Flutter, `integration_test`, Patrol) provides one |

**Takeaway:** no canonical "test report" schema exists in the Flutter
world at all — it's raw JSON events, ad-hoc Markdown+screenshots (as in
DroidAgent-style tools), or a homegrown in-memory audit log. A tool that
already ships one structured JSON report per run (slmtest, today) is
ahead of this ecosystem, not behind it.

### Python/web ecosystem

| Tool | Report format | Notes |
|---|---|---|
| [JUnit XML](https://github.com/testmoapp/junitxml) | `testsuite`(`time`, `timestamp`) → `testcase`(`classname`, `name`, `time`) → `failure`/`error`/`skipped`/`system-out` | The de facto CI interchange format precisely *because* it's simple and every language already has a writer for it. Nearly every CI system (GitHub Actions, GitLab, Jenkins, CircleCI, Azure DevOps) natively turns it into inline PR annotations and a test-result tab. Lowest implementation effort of everything surveyed here |
| [pytest](https://docs.pytest.org/) | `--junitxml` (standard JUnit output) | [`pytest-html`](https://pytest-html.readthedocs.io/)'s `extra` mechanism — attach a screenshot/text/json per test result via a `conftest.py` hook, typically gated to failures only — is the most directly reusable pattern for "attach a screen snapshot to a step result" |
| [Playwright traces](https://playwright.dev/docs/trace-viewer) | a `trace.zip` per run: DOM snapshots before/during/after every action, network HAR, console log, screencast | **The single most relevant prior art found.** Replayable via `npx playwright show-trace` or the static client-side viewer at trace.playwright.dev. `test.step()` gives genuinely nested step-level pass/fail inside one test. CI only needs to upload the zip as a build artifact (commonly gated to `on-first-retry` to bound size). This is the shape of a real "audit trail" for a UI test — a single replayable bundle, not just a text transcript |
| Selenium/WebDriver | none built in | Screenshot-on-failure and video-via-sidecar-container are conventions every team bolts on individually — worth noting as "what not to do": it leaves every team reinventing the same hook |
| [Allure](https://allurereport.org/) | per-test/per-step JSON result files (`{uuid}-result.json`) with `start`/`stop` timestamps, `status`, recursively-nested `steps[]`, `attachments[]`, and a `historyId` for cross-run trend/flaky tracking | The most complete prior art for **both** step-level detail **and** audit-trail-across-runs. Its schema is simple enough (JSON, not XML) that a Go tool could plausibly emit Allure-compatible result files and get a polished existing viewer/trend UI for free |
| TAP | plain text protocol | Mentioned for completeness; not a serious contender — simpler than what's already true of slmtest's `-json`, and JUnit XML already covers the same "any CI can consume it" niche better |

**LLM-driven testing frameworks with their own report format:** none
found. Playwright's own "Test Agents" (Planner/Generator/Healer,
Playwright 1.56) use an LLM only to *author* conventional Playwright
tests ahead of time — once generated, they run and report exactly like
any other Playwright test. Stagehand/browser-use wrap Playwright but
emit Playwright's own reporter/trace output, not a novel schema. **This
is a genuine gap:** a tool whose whole value is "LLM-in-the-loop UI
testing with its own step-level report" has limited direct competition,
which argues for interoperating with existing formats (JUnit XML,
Allure-shaped JSON) rather than inventing a fourth proprietary one.

## Recommended roadmap

A phased sequence, cheapest/lowest-risk first. Each phase is additive to
the existing `-json` contract — never breaking it, since CLAUDE.md
already treats that shape as a CI contract consumed by tooling.

### Phase A — Audit metadata — **implemented**

Added `started_at`/`finished_at`/`duration_ms` (run, per-step, and
per-turn), and a `run_context` block (model, endpoint, driver,
temperature, native-tools/sandboxed flags, `slmtest` version, git commit/
dirty flag, hostname) to `Report`/`StepOutcome`, plus a per-turn full
screen snapshot (`TurnLog.Screen` / `driver.Observation.Screen`). Closed
the single biggest gap found in every comparison above — notably,
Flutter's own ecosystem doesn't even have this today. See CLAUDE.md,
"The `-json` report shape," for the shipped field-by-field design.

### Phase B — JUnit XML export — **implemented**

A `-junit <path>` flag (CLI) / `junit_path` MCP param
(`internal/runner/junit.go`'s `MarshalJUnit`), mapping `Test`→
`testsuite`, `Step`→`testcase` (`classname` = test name, `name` = step
title, `time` = step duration from Phase A), `Reason` into `<failure
message=...>`/`<error message=...>` (split by `StepOutcome.Status()`),
and a compact per-turn summary into `<system-out>`. See CLAUDE.md,
"CI/audit-trail artifacts."

### Phase C — A replayable trace bundle — **implemented**

Playwright-trace-inspired: a `-trace <dir>` option / `trace_dir` MCP
param (`internal/cliops/trace.go`'s `writeTraceBundle`) that, alongside
the JSON report, persists each turn's Phase A screen snapshot as a
discrete, indexed file, plus a `manifest.json` tying step→turn→snapshot
together and a full `report.json` per test. Built entirely on Phase A's
`TurnLog.Screen` — no new capture mechanism, only persistence + a
manifest, exactly as scoped below.

### Phase D — Golden-file regression mode — **implemented**

Borrowing Alchemist's CI-vs-local split: a `-golden <dir>`/
`-golden-update` option (`internal/cliops/golden.go`'s `compareGolden`)
that persists each step's last-captured screen (the state at
`finish_step` time) as a checked-in baseline and diffs future runs
against it, for regression-testing a TUI/UI's visual output over time
independent of the SLM's own judgement. As scoped: it deliberately does
not affect a step's pass/fail verdict or the process exit code — the
model-driven verdict stays the core value proposition; golden-diffing is
a complement, not a replacement. No diff-engine dependency (first
differing line only) and no `-golden-strict` flag, matching the original
scope.

### Phase E — Agent-authored test suites — **scoped down to
documentation; "record mode" deliberately not built**

The original text below proposed two tiers. Only the first was
implemented; the second was evaluated and explicitly declined once
Phases A–C's audit trail existed to weigh it against:

- **Already possible today, now documented:** an agent with shell access
  and `slmtest-mcp`'s `validate_test`/`init_test` can explore a
  TUI/browser UI by hand (running commands, reading output) and author a
  `.md` spec iteratively, validating after every edit. Written up in
  `USAGE.md`, "Agent-authoring a spec: explore, draft, validate, run,"
  and "Reviewing what happened."
- **"Record mode" (a human/agent live-driving the PTY/browser directly,
  bypassing the SLM turn loop, to auto-generate a starter spec):
  evaluated, not built.** The decision, made explicitly rather than by
  default: Phases A–C's richer per-turn audit trail (full screen
  snapshots, timestamps, run context, a replayable `-trace` bundle)
  already answers the underlying question this project actually needed
  answered — "what did the model see and decide" for a run that already
  happened — without needing a live-drive/steering feature at all. A
  record mode would also be a materially larger, separate effort: no
  raw-PTY-passthrough plumbing exists anywhere in this codebase today
  (confirmed by exploration before this decision), and building it would
  mean designing a second input path alongside the SLM's own, not a
  small addition to the existing turn loop. If a genuine need for
  live-drive-and-record authoring emerges later, it should get its own
  design pass rather than being folded into this one.

## Original next step (superseded)

The original recommendation — *"Pick which of Phase A–E to scope as the
next real implementation plan; Phase A is the natural starting point"* —
was acted on in full for A–D and partially (documentation only) for E;
see the "Status" line at the top of this document.
