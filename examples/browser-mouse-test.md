---
name: browser-mouse-test
description: Exercises the Phase B mouse primitives against a real Chromium page — double_click, right_click, and drag, each verified against the real DOM, not assumed. Requires a build with `-tags browserdriver` and the page's URL passed via `-driver-option url=file:///ABSOLUTE/PATH/TO/examples/browser-mouse.html`.
driver: browser
timeout_seconds: 300
max_turns_per_step: 6
---

## Step 1: Confirm the page loaded
Goal: the browser has loaded the mouse demo page with nothing acted on yet.
Expect: the page snapshot's title mentions "mouse demo" and its visible text shows the status as "idle".

## Step 2: Double-click the target
Goal: the status text changes to reflect a real double-click, not a single click.
Hint: use the double_click action with target "#dblclick-target".
Expect: the page's visible text now contains "double-clicked".

## Step 3: Right-click the target
Goal: the status text changes to reflect a real right-click.
Hint: use the right_click action with target "#rightclick-target".
Expect: the page's visible text now contains "right-clicked".

## Step 4: Drag the source onto the drop zone
Goal: dragging "#drag-source" onto "#drop-zone" fires the drop handler.
Hint: use the drag action with from "#drag-source" and to "#drop-zone".
Expect: the page's visible text now contains "dropped" — proving the drag actually completed, not just that the action ran without error.
