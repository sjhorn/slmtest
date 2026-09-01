---
name: task-board-test
description: A richer browser-driver QA script than browser-mouse-test.md — typed input, keyboard-only interaction (no click), drag between two distinct drop targets, keyboard deletion, and a final ground-truth check via a DOM counter the actions never touch directly. Requires a build with `-tags browserdriver` and the page's URL passed via `-driver-option url=file:///ABSOLUTE/PATH/TO/examples/task-board.html`.
driver: browser
timeout_seconds: 600
max_turns_per_step: 6
---

## Step 1: Add a task by typing
Goal: a new task titled "Buy milk" appears in the To Do column, and the input field is empty again afterward.
Hint: click "#new-task", use type_text to enter "Buy milk", then press_key "enter" to submit it.
Expect: the page's visible text shows "Buy milk" under the To Do heading, and the counter near the top reads "Tasks remaining: 1".

## Step 2: Mark the task done using only the keyboard
Goal: "Buy milk"'s checkbox becomes checked without clicking it — the checkbox already has keyboard focus right after step 1.
Hint: use press_key "space" (no click, no target — the checkbox is already focused).
Expect: the visible text for that task now reads "Buy milk (done)", proving a real keypress reached the real page and toggled real checkbox state.

## Step 3: Add a second task and drag it directly to Done
Goal: a second task, "Walk dog", ends up listed under the Done column without ever having its checkbox checked.
Hint: click "#new-task", type_text "Walk dog", press_key "enter" to add it, then use the drag action with from the new task's list-item selector and to "#done-list".
Expect: the page's visible text shows "Walk dog" under the Done heading and no longer under the To Do heading. "Buy milk (done)" is still under To Do.

## Step 4: Delete the first task via the keyboard
Goal: "Buy milk" is removed from the page entirely, using the Delete key rather than any click-to-remove control (there isn't one).
Hint: click the task's label text (selector ending in "-label") to focus its row, then press_key "delete".
Expect: the page's visible text no longer contains "Buy milk" anywhere on the page.

## Step 5: Confirm the ground-truth counter, not just the visible list
Goal: the page's own counter — updated independently by a DOM mutation, not by anything the previous steps directly asserted — agrees that no tasks remain in To Do.
Expect: the visible text contains "Tasks remaining: 0". (Walk dog was dragged to Done, not deleted, and does not count against this.)
