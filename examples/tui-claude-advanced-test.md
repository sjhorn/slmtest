---
name: tui-claude-advanced-test
description: Drive Claude Code through a real multi-step coding task — plan, tracked tasks, real work — verified against the filesystem, not the screen
shell: /bin/bash
term: xterm-256color
size: 40x120
timeout_seconds: 1800
max_turns_per_step: 12
---

## Step 1: Create a folder Claude Code has not seen before
Goal: a freshly created, uniquely-named directory exists and is the current working directory — one Claude Code cannot already have a trust decision recorded for.
Hint: export TUIDIR=$(mktemp -d /tmp/slmtest-claude-adv-XXXXXX) && cd "$TUIDIR" && pwd
Expect: `pwd` prints a path starting with /tmp/slmtest-claude-adv- that did not exist before this command ran. Do not reuse a fixed path here — see tui-claude-test.md's step 1 for why.

## Step 2: Launch the Claude Code TUI
Goal: the `claude` TUI is running and has drawn its interface.
Hint: claude
Expect: the terminal shows a full-screen interface drawn with box-drawing characters (─ │ ╭ ╮ ╰ ╯). Because this folder is new, it asks a trust question with a numbered menu. It may take several seconds to appear, so use the wait action rather than re-running the command if nothing has rendered yet. Do NOT answer the trust question in this step — finish_step as soon as the menu is visible; a later step handles answering it. If `claude` appears to do nothing after you already see the menu, that is normal: the TUI is waiting on you, not stuck, so do not re-run `claude` or try other commands.

## Step 3: Trust the folder
Goal: the folder has been trusted and Claude Code has moved past the trust prompt to its normal input screen.
Hint: the menu already defaults to "1. Yes, I trust this folder" (shown with a ❯ marker) — run_command with command "" presses Enter alone and confirms it.
Expect: the trust question and its numbered options are gone, and an input prompt or text box is visible, ready to accept a message.

## Step 4: Give Claude a real, multi-part coding task
Goal: the task has been submitted and Claude has started working on it.
Hint: run_command with command "Create greet.py with a function greet(name) that returns the string 'Hello, {name}!', and a __main__ block that prints greet('World') when the script is run directly. Also create test_greet.py with a test that asserts greet('World') returns 'Hello, World!'. Break this into a short task list before starting, then complete every task, then run the test to confirm it passes."
Expect: the input has been submitted (it is no longer sitting in the input box) and the terminal shows Claude has begun responding — a spinner/status indicator (characters like ✻ ✶ ✳ ✢ · or words like "Considering…") or the start of tool-use output.

## Step 5: Confirm a tracked task list appeared
Goal: Claude broke the work into multiple distinct, individually-tracked tasks, visible on screen.
Hint: none needed — wait if nothing has rendered yet. Claude Code shows tracked tasks as a checklist-style list (markers like ☐/☑ or ⎿, one line per task).
Expect: at least two distinct task/todo items are visible in the terminal output, each describing a different piece of the work (e.g. one for greet.py, one for test_greet.py, one for running the test). This terminal only ever shows what changed since your last action, not the whole current screen — read whatever is visible in the current turn's output rather than waiting for the same list to reappear unchanged.

## Step 6: Wait for Claude to finish all tasks
Goal: Claude has finished working — every tracked task is complete and Claude is idle, waiting for the next input.
Hint: use wait with a generous wait_ms (10000-30000) between checks; real multi-file work with a test run can take a minute or more. Do not resend the task. Claude Code may pause partway through to ask permission before creating or editing a file (a menu like "Do you want to proceed? 1. Yes  2. Yes, and always allow... 3. No"). The numbers are position labels, not keyboard shortcuts -- digit keys do nothing here. Send a Down-arrow keystroke (send_keys with command "\u001b[B") to move the highlighted option to "2. Yes, and always allow...", then run_command with command "" to press Enter and confirm it -- this avoids being asked again for the rest of this task. If it only asks once and a bare Enter on the default already unblocks it, that is fine too.
Expect: the busy spinner/status indicator is gone, an idle input prompt (❯) is visible with nothing running, and the visible task list (if still shown) has no items left unmarked. As with step 5, decide from whatever turn this first becomes visible in — it will not be re-shown once you wait past it.

## Step 7: Exit the TUI
Goal: Claude Code has exited and an ordinary shell prompt is visible again.
Hint: run_command with command "/exit" — a normal typed command, submitted with Enter like any other. Avoid Ctrl-C/Ctrl-D here: Claude Code needs two presses in quick succession to confirm an exit that way, and the gap between two separate actions in this harness is usually too slow to land within that window.
Expect: the TUI's box-drawing interface is gone and an ordinary shell prompt is visible again.

## Step 8: Confirm greet.py was actually created correctly
Goal: greet.py exists on disk and defines the requested function — checked against the filesystem, not by trusting what the TUI claimed.
Hint: cat "$TUIDIR/greet.py"
Expect: the file exists and its contents define a function greet(name) that returns a string containing "Hello," and the name, and a __main__ block.

## Step 9: Confirm greet.py actually runs correctly
Goal: running the script produces the expected output — real execution, not a claim.
Hint: python3 "$TUIDIR/greet.py"
Expect: the output is exactly "Hello, World!" (or contains it on its own line).

## Step 10: Confirm the test file exists and actually passes
Goal: test_greet.py exists and genuinely passes when run — real execution, not a claim.
Hint: cd "$TUIDIR" && python3 -m pytest test_greet.py -q || python3 -m unittest test_greet.py -v
Expect: the test run reports success (e.g. "1 passed", "OK", or exit 0) with no failure or error reported.

## Step 11: Clean up
Goal: the directory created in step 1 no longer exists.
Hint: cd /tmp && rm -rf "$TUIDIR"
Expect: `ls -d "$TUIDIR"` reports that the path does not exist.
