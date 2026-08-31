---
name: browser-form-test
description: A more complex browser-driver spec than browser-test.md — a multi-field form (click + type_text into two separate fields, with the typed values echoed back into the real DOM) followed by the driver's bespoke navigate action to a second page. Requires a build with `-tags browserdriver` and the form page's URL passed via `-driver-option url=file:///ABSOLUTE/PATH/TO/examples/browser-contact-form.html`.
driver: browser
timeout_seconds: 600
max_turns_per_step: 6
---

## Step 1: Confirm the contact form loaded
Goal: the browser has loaded the contact form page with both fields empty.
Expect: the page snapshot's title mentions "contact form demo" and shows both a Name and an Email input, with no result message yet.

## Step 2: Fill in the name field
Goal: the Name field contains the text "Ada Lovelace".
Hint: click the element with selector "#name-field", then use type_text to enter "Ada Lovelace".
Expect: the snapshot's "Interactive elements" list shows the name field's own entry as `[input] "Ada Lovelace" -> #name-field` — the typed value now shown as that element's label, not just the click/type_text actions having run without error.

## Step 3: Fill in the email field
Goal: the Email field contains the text "ada@example.com".
Hint: click the element with selector "#email-field", then use type_text to enter "ada@example.com".
Expect: the snapshot's "Interactive elements" list shows the email field's own entry as `[input] "ada@example.com" -> #email-field`.

## Step 4: Submit and confirm the real DOM updated
Goal: clicking Submit reveals a confirmation that echoes back exactly what was typed.
Hint: click the element with selector "#submit-btn".
Expect: the page's visible text now contains "Thanks, Ada Lovelace! We will reach you at ada@example.com." — proving both typed values actually reached the form fields (a form that read empty fields would show the error message instead).

## Step 5: Navigate to the confirmation page
Goal: the browser has navigated away from the form to a separate confirmation page.
Hint: use the navigate action with url "browser-contact-thankyou.html" (relative to the current page).
Expect: the page snapshot's title mentions "contact confirmation" and its visible text contains "SLM-4471".
