---
name: login-flow-feature
description: "Level 2 of the BDD-format investigation (see docs/model-runs.md): a Feature file with a shared Background and two independent Scenarios, using the optional Feature/Background/Scenario markdown layer (internal/spec/feature.go). Requires a build with `-tags browserdriver` and the fixture's URL passed via `-driver-option url=file:///ABSOLUTE/PATH/TO/examples/login-flow.html`."
driver: browser
timeout_seconds: 300
max_turns_per_step: 6
---

## Background
### Step 1: Given a registered user on the sign-in page
Goal: the sign-in page is loaded, showing empty username and password fields and no message yet.
Expect: the page snapshot's title mentions "login flow demo" and shows a Username field, a Password field, and a Log in button, with no message text below the form.

@smoke
## Scenario: Successful login
### Step 1: When they submit the correct username and password
Goal: the login form is submitted with the valid credentials.
Hint: click "#username-field", type_text "alice", click "#password-field", type_text "wonderland123", then click "#login-btn".
Expect: the browser has navigated away from the sign-in page.

### Step 2: Then they see a welcome message on the dashboard
Goal: the user lands on the dashboard page and sees a personalized welcome.
Expect: the page snapshot's title mentions "login dashboard demo" and its visible text contains "Welcome back, alice!".

## Scenario: Login fails with an incorrect password
### Step 1: When they submit the correct username but the wrong password
Goal: the login form is submitted with the correct username and an incorrect password.
Hint: click "#username-field", type_text "alice", click "#password-field", type_text "wrongpassword", then click "#login-btn".
Expect: the page's visible text contains "Invalid username or password." and the browser is still on the sign-in page (the title still mentions "login flow demo").
