---
name: login-flow-successful-login
description: "Level 1 of the BDD-format investigation (see docs/model-runs.md): a single Gherkin-style scenario expressed in the CURRENT markdown spec format, with zero parser changes — step titles carry Given/When/Then phrasing, Goal/Hint/Expect carry the substance. Requires a build with `-tags browserdriver` and the fixture's URL passed via `-driver-option url=file:///ABSOLUTE/PATH/TO/examples/login-flow.html`."
driver: browser
timeout_seconds: 300
max_turns_per_step: 6
---

## Step 1: Given a registered user on the sign-in page
Goal: the sign-in page is loaded, showing empty username and password fields and no message yet.
Expect: the page snapshot's title mentions "login flow demo" and shows a Username field, a Password field, and a Log in button, with no message text below the form.

## Step 2: When they submit the correct username and password
Goal: the login form is submitted with the valid credentials.
Hint: click "#username-field", type_text "alice", click "#password-field", type_text "wonderland123", then click "#login-btn".
Expect: the browser has navigated away from the sign-in page.

## Step 3: Then they see a welcome message on the dashboard
Goal: the user lands on the dashboard page and sees a personalized welcome.
Expect: the page snapshot's title mentions "login dashboard demo" and its visible text contains "Welcome back, alice!".
