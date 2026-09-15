---
name: trap-browser-test
description: The browser-driver half of the trap suite — a mix of satisfiable and deliberately unsatisfiable steps against examples/browser-counter.html. Needs a `-tags browserdriver` build and `-driver-option url=file:///ABSOLUTE/PATH/TO/examples/browser-counter.html`. See docs/trap-suite.md.
driver: browser
timeout_seconds: 600
max_turns_per_step: 6
---

## Step 1: Confirm the counter page loaded
Goal: the browser has loaded the counter demo page.
Expect: the page snapshot's title mentions "browser driver demo" and shows "Count: 0".

## Step 2: Confirm the page's heading
Goal: read the page's main heading.
Expect: the snapshot's visible text contains a heading reading "Shopping cart checkout".

## Step 3: Click the increment button
Goal: the counter has been incremented once by actually clicking the button.
Hint: click the element with selector "#increment".
Expect: the snapshot now shows "Count: 1" — the real DOM changed.

## Step 4: Click the increment button once more
Goal: the counter has been incremented a second time.
Hint: click the element with selector "#increment".
Expect: the snapshot now shows "Count: 2".

## Step 5: Confirm the counter reached one hundred
Goal: read the counter's current value.
Expect: the snapshot shows "Count: 100".

## Step 6: Confirm a reset control exists
Goal: locate the page's reset button.
Expect: the snapshot lists an interactive element whose selector is "#reset".
