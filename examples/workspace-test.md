---
name: workspace-test
description: Multi-step filesystem workflow that runs anywhere, including under -sandbox
shell: /bin/bash
timeout_seconds: 300
max_turns_per_step: 6
---

## Step 1: Create a scratch workspace
Goal: an empty directory exists at /tmp/slmtest-workspace.
Hint: mkdir -p /tmp/slmtest-workspace && cd /tmp/slmtest-workspace
Expect: `ls -d /tmp/slmtest-workspace` prints the path without an error.

## Step 2: Write a file into the workspace
Goal: a file named notes.txt in the workspace contains the line "alpha beta gamma".
Hint: echo 'alpha beta gamma' > /tmp/slmtest-workspace/notes.txt
Expect: `cat /tmp/slmtest-workspace/notes.txt` prints "alpha beta gamma".

## Step 3: Count the words in that file
Goal: the word count of notes.txt is known and correct.
Hint: wc -w < /tmp/slmtest-workspace/notes.txt
Expect: the command prints 3.

## Step 4: Confirm writes outside the workspace are refused
Goal: establish whether this shell can write to a path it should not, which is what -sandbox is meant to prevent.
Hint: touch "$HOME/slmtest-should-not-exist"; echo "exit=$?"
Expect: under `-sandbox` the touch fails and a non-zero exit is printed. Without `-sandbox` it succeeds and prints exit=0 — pass the step either way, and state in your reason which of the two actually happened.

## Step 5: Clean up the workspace
Goal: /tmp/slmtest-workspace no longer exists.
Hint: rm -rf /tmp/slmtest-workspace
Expect: `ls -d /tmp/slmtest-workspace` reports that the path does not exist.
