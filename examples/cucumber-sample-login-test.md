---
name: cucumber-sample-login
description: "A real Cucumber .feature file, not authored for this project, translated into this markdown dialect. Source: Minds/mobile-native, e2e/modules/login/Login.feature (github.com/Minds/mobile-native) — a mobile app's login flow test. The structure (Background, a plain Scenario, a tagged-free Scenario Outline with an Examples table, a closing Scenario) is an unmodified transcription of the original file's shape; step wording is adapted from Gherkin's Given/When/Then into Goal/Hint/Expect, and the two Scenarios specific to the original app's backend (banned/deleted accounts) were dropped — this project's login-flow.html fixture has no backend to express those states against. Requires a build with `-tags browserdriver` and the fixture's URL passed via `-driver-option url=file:///ABSOLUTE/PATH/TO/examples/login-flow.html`."
driver: browser
timeout_seconds: 400
max_turns_per_step: 6
---

## Background
### Step 1: Given I'm logged out, on the login page
Goal: the sign-in page is loaded, showing empty username and password fields and no message yet.
Expect: the page snapshot's title mentions "login flow demo" and shows a Username field, a Password field, and a Log in button, with no message text below the form.

## Scenario: Displays error when logging in with empty credentials
### Step 1: When I try to log in with credentials; username: "" and password: ""
Goal: the login form is submitted with both fields left empty.
Hint: click "#login-btn" without typing anything into either field first.
Expect: the page shows a validation message stating a required field is missing (this fixture reports "Username is required." first).

## Scenario Outline: Displays warning message when logging in with invalid credentials
### Step 1: When I try to log in with credentials; username: "<Username>" and password: "<Password>"
Goal: the login form is submitted with a made-up, non-registered username and password.
Hint: click "#username-field", type_text "<Username>", click "#password-field", type_text "<Password>", then click "#login-btn".
Expect: the page shows "Invalid username or password." and remains on the sign-in page.

### Examples
| Username    | Password  |
|-------------|-----------|
| user@123456 | 12346.346 |
| 3243g@user  | /12346569 |

## Scenario: Should successfully login with a valid user
### Step 1: When I try to log in with a valid channel
Goal: the login form is submitted with the one registered account this fixture recognizes.
Hint: click "#username-field", type_text "alice", click "#password-field", type_text "wonderland123", then click "#login-btn".
Expect: the browser navigates away from the sign-in page to the dashboard, whose visible text contains "Welcome back, alice!".
