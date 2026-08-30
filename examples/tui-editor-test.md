---
name: tui-editor-test
description: Drive vi through the PTY — full-screen TUI, modal input, and a control character
shell: /bin/bash
term: xterm-256color
size: 24x80
timeout_seconds: 600
max_turns_per_step: 8
---

## Step 1: Open a file in vi
Goal: vi is running full-screen on /tmp/slmtest-tui.txt and waiting for input.
Hint: vi /tmp/slmtest-tui.txt
Expect: the screen shows vi's empty-buffer display — a column of `~` characters down the left edge. The shell prompt is no longer visible because vi has taken over the terminal.

## Step 2: Enter insert mode and type a line
Goal: the text "tui input works" has been typed into the buffer.
Hint: use send_keys with command "i", then a second send_keys with the text. send_keys does not press Enter, which is what you want here.
Expect: the typed text is visible in the buffer, and vi shows its insert-mode indicator (`-- INSERT --`) near the bottom of the screen. Do not press Enter — a newline is not needed and would add a blank line.

## Step 3: Leave insert mode
Goal: vi is back in normal mode, ready for an ex command.
Hint: use send_keys with command "\u001b" (the escape character).
Expect: the `-- INSERT --` indicator is gone from the bottom of the screen.

## Step 4: Save and quit
Goal: vi has written the file and exited back to the shell.
Hint: :wq
Expect: vi's display is gone and an ordinary shell prompt is visible again.

## Step 5: Confirm the file was actually written
Goal: the text typed inside the TUI reached the filesystem.
Hint: cat /tmp/slmtest-tui.txt
Expect: the output contains "tui input works".

## Step 6: Clean up
Goal: /tmp/slmtest-tui.txt no longer exists.
Hint: rm -f /tmp/slmtest-tui.txt
Expect: `ls /tmp/slmtest-tui.txt` reports that the file does not exist.
