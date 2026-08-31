---
name: browser-test
description: Drives a real Chromium page via the "browser" driver — click a button, confirm the DOM actually updated. Requires a build with `-tags browserdriver` (Playwright + Chromium installed; see internal/browserdriver's package doc) and the local page's URL passed via `-driver-option url=file:///ABSOLUTE/PATH/TO/examples/browser-counter.html`.
driver: browser
timeout_seconds: 120
max_turns_per_step: 6
---

## Step 1: Confirm the counter page loaded
Goal: the browser has loaded the counter demo page.
Expect: the page snapshot's title mentions "browser driver demo" and shows "Count: 0".

## Step 2: Click the increment button
Goal: the counter has been incremented once by actually clicking the button, not by assuming.
Hint: click the element shown as "[button]" with selector "#increment" in the snapshot.
Expect: the snapshot now shows "Count: 1" — the real DOM changed, not just an assumption that the click worked.
