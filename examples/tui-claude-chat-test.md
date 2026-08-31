---
name: tui-claude-chat-test
description: Drive the Claude Code TUI through trusting a folder and one real turn of conversation
shell: /bin/bash
term: xterm-256color
size: 40x120
timeout_seconds: 900
max_turns_per_step: 8
---

## Step 1: Create a folder Claude Code has not seen before
Goal: a freshly created, uniquely-named directory exists and is the current working directory — one Claude Code cannot already have a trust decision recorded for.
Hint: export TUIDIR=$(mktemp -d /tmp/slmtest-claude-chat-XXXXXX) && cd "$TUIDIR" && pwd
Expect: `pwd` prints a path starting with /tmp/slmtest-claude-chat- that did not exist before this command ran. Do not reuse a fixed path here — see tui-claude-test.md's step 1 for why.

## Step 2: Launch the Claude Code TUI
Goal: the `claude` TUI is running and has drawn its interface.
Hint: claude
Expect: the terminal shows a full-screen interface drawn with box-drawing characters (─ │ ╭ ╮ ╰ ╯). Because this folder is new, it asks a trust question — "Is this a project you created or one you trust?" — with a numbered menu. It may take several seconds to appear, so use the wait action rather than re-running the command if nothing has rendered yet.

## Step 3: Trust the folder
Goal: the folder has been trusted and Claude Code has moved past the trust prompt to its normal input screen.
Hint: the menu already defaults to "1. Yes, I trust this folder" (shown with a ❯ marker) — run_command with command "" presses Enter alone and confirms it.
Expect: the trust question and its numbered options are gone, and an input prompt or text box is visible, ready to accept a message.

## Step 4: Send one message and read the reply
Goal: Claude has replied to a short, harmless prompt.
Hint: run_command with command "Reply with only the single word banana, nothing else." — a normal typed line, submitted with Enter like any other command. Do not add your own newline inside the command text; run_command already submits it. A real model response can take up to 20-30 seconds — use additional wait actions rather than re-sending the message.
Expect: the terminal shows a reply from Claude containing the word "banana" (case-insensitive) after the prompt was submitted. The reply is often marked with a "⏺" bullet and can appear in the same turn as trailing spinner-animation noise (characters like ✻ ✶ ✳ ✢ or words like "Stewing…"/"Osmosing…") — read that output carefully rather than waiting past it. This terminal only ever shows what changed since your last action, not the whole current screen, so a reply visible in one turn's output will not be shown again in a later one if nothing further changes — decide from the turn where it first appears rather than waiting for it to reappear.

## Step 5: Exit the TUI
Goal: Claude Code has exited and an ordinary shell prompt is visible again.
Hint: run_command with command "/exit" — a normal typed command, submitted with Enter like any other. Avoid Ctrl-C/Ctrl-D here: Claude Code needs two presses in quick succession to confirm an exit that way, and the gap between two separate actions in this harness is usually too slow to land within that window.
Expect: the TUI's box-drawing interface is gone and an ordinary shell prompt is visible again.

## Step 6: Confirm the shell is usable again
Goal: the terminal has been handed back cleanly, with no leftover TUI state.
Hint: echo tui-chat-exited-cleanly
Expect: the output contains "tui-chat-exited-cleanly" on an ordinary shell prompt.

## Step 7: Clean up
Goal: the directory created in step 1 no longer exists.
Hint: cd /tmp && rm -rf "$TUIDIR"
Expect: `ls -d "$TUIDIR"` reports that the path does not exist.
