---
name: nano-edit-test
description: A richer TUI QA script than tui-editor-test.md — nano's status-bar-driven UI (not vi's modal one), a cut/paste round-trip, an in-editor search, and a save that must be confirmed via a pre-filled prompt, using press_key's Phase B ctrl-modifier support (Ctrl+K/Ctrl+U/Ctrl+W/Ctrl+O/Ctrl+X) instead of raw control bytes. Ends with a ground-truth check of the saved file, not the screen.
shell: /bin/bash
term: xterm-256color
size: 30x100
timeout_seconds: 400
max_turns_per_step: 8
---

## Step 1: Launch nano on a fresh file
Goal: nano is running full-screen, editing a brand-new temp file.
Hint: run_command with command `export NANOFILE=$(mktemp /tmp/slmtest-nano-XXXXXX.txt) && nano "$NANOFILE"`
Expect: the screen shows a reverse-video title bar naming the file and a two-row menu of `^`-prefixed commands (Get Help, WriteOut, Read File, etc.) across the bottom — nano has taken over the terminal.

## Step 2: Type three lines of text
Goal: the buffer contains exactly these three lines, in order: "first line of the file", "second line to search for", "third line at the end" — with the cursor now sitting on a new, blank fourth line.
Hint: use send_keys three times, each with the line's text and press_enter true, so each line is followed by pressing Enter.
Expect: all three lines are visible in the editor buffer in that order, and the title bar now shows "Modified".

## Step 3: Cut the last line, then paste it back
Goal: "third line at the end" is temporarily removed with Cut Text, then restored with UnCut Text, ending up visible again in the buffer.
Hint: the cursor is currently on the blank line below the typed text, not on a real line — press_key "up" once first to move onto "third line at the end". Then use press_key with key "k" and modifiers ["ctrl"] (Ctrl+K, Cut Text — the line should disappear). Then use press_key with key "u" and modifiers ["ctrl"] (Ctrl+U, UnCut Text) to paste it back immediately.
Expect: "third line at the end" is visible in the buffer again by the end of this step (it disappearing briefly mid-step, right after the cut, is expected and fine).

## Step 4: Search for a word
Goal: nano's own search (not eyeballing the screen) locates the line containing "search".
Hint: use press_key with key "w" and modifiers ["ctrl"] (Ctrl+W, "Where is") to open the search prompt, then run_command with command "search" to type the search term and submit it with Enter.
Expect: the search prompt is gone, replaced by the ordinary editing view, and no "not found" message is shown — a wrapped-search notice, if any, is fine.

## Step 5: Save the file
Goal: the buffer's current contents are written to disk.
Hint: use press_key with key "o" and modifiers ["ctrl"] (Ctrl+O, WriteOut) to open the save prompt — it comes pre-filled with the file's own path — then run_command with an empty command to confirm with Enter.
Expect: a "Wrote" confirmation naming a number of lines appears, and the title bar no longer shows "Modified".

## Step 6: Exit nano
Goal: nano has closed and an ordinary shell prompt is visible again.
Hint: use press_key with key "x" and modifiers ["ctrl"] (Ctrl+X, Exit). Since the file was just saved with no changes since, nano should exit directly without asking to save again.
Expect: the full-screen editor interface (the reverse-video title bar and bottom menu) is gone, and a normal shell prompt is visible.

## Step 7: Confirm the saved file on disk
Goal: the file actually contains all three original lines, in order — proof the edit/cut/paste/save sequence worked, independent of anything the screen showed.
Hint: run_command with command `cat "$NANOFILE"`
Expect: the output contains, in order, "first line of the file", "second line to search for", and "third line at the end".

## Step 8: Clean up
Goal: the temp file no longer exists.
Hint: run_command with command `rm "$NANOFILE"`
Expect: `ls "$NANOFILE"` reports that the file does not exist.
