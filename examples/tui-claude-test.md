---
name: tui-claude-test
description: Drive the Claude Code TUI — a real modern terminal UI, without starting a session
shell: /bin/bash
term: xterm-256color
size: 40x120
timeout_seconds: 900
max_turns_per_step: 8
---

## Step 1: Create a folder Claude Code has not seen before
Goal: a freshly created, uniquely-named directory exists and is the current working directory — one Claude Code cannot already have a trust decision recorded for, because no prior run ever used this exact name.
Hint: export TUIDIR=$(mktemp -d /tmp/slmtest-claude-tui-XXXXXX) && cd "$TUIDIR" && pwd
Expect: `pwd` prints a path starting with /tmp/slmtest-claude-tui- that did not exist before this command ran. Do not reuse a fixed path here — a fixed path can get permanently marked as trusted by an earlier run (Claude Code remembers trust decisions by path in `~/.claude.json`, independent of whether the directory still exists on disk), which would silently skip the trust prompt every later step in this test depends on.

## Step 2: Launch the Claude Code TUI
Goal: the `claude` TUI is running and has drawn its interface.
Hint: claude
Expect: the terminal shows a full-screen interface drawn with box-drawing characters (─ │ ╭ ╮ ╰ ╯). Because this folder is new, it asks a trust question — "Is this a project you created or one you trust?" — with a numbered menu. It may take several seconds to appear, so use the wait action rather than re-running the command if nothing has rendered yet.

## Step 3: Read the menu the TUI is offering
Goal: the two choices the trust prompt offers are identified from the already-rendered screen, without pressing any keys.
Hint: none needed — the screen is already showing. Use the wait action if you need to look again.
Expect: the screen shows two numbered options, one to trust the folder and one to decline and exit, plus a footer describing how to confirm or cancel.

## Step 4: Decline and leave the TUI
Goal: Claude Code has exited without the folder being trusted and without any session being started.
Hint: use the press_key action with params {"key": "escape"} — the footer offers Esc to cancel, and press_key handles translating the logical key name to the raw byte for you (no need to know the byte sequence yourself). If that does not exit, send_keys with command "\u0003" (Ctrl-C) instead.
Expect: the TUI is gone and an ordinary shell prompt is visible again. Do NOT choose the option that trusts the folder, and do not type a message to Claude — this test only exercises the terminal interface.

## Step 5: Confirm the shell is usable again
Goal: the terminal has been handed back cleanly, with no leftover TUI state.
Hint: echo tui-exited-cleanly
Expect: the output contains "tui-exited-cleanly" on an ordinary shell prompt.

## Step 6: Clean up
Goal: the directory created in step 1 no longer exists.
Hint: cd /tmp && rm -rf "$TUIDIR"
Expect: `ls -d "$TUIDIR"` reports that the path does not exist.
