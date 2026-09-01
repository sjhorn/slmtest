---
name: login-validation-outline
description: "Level 3 of the BDD-format investigation (see docs/model-runs.md): a Scenario Outline with an Examples data table, expanded into one independent scenario per row — the same role Cucumber's Scenario Outline/Examples play. Requires a build with `-tags browserdriver` and the fixture's URL passed via `-driver-option url=file:///ABSOLUTE/PATH/TO/examples/login-flow.html`."
driver: browser
timeout_seconds: 300
max_turns_per_step: 6
---

## Scenario Outline: Login rejects invalid input
### Step 1: When they submit "<username>" and "<password>"
Goal: the login form is submitted with the given username and password.
Hint: click "#username-field", type_text "<username>", click "#password-field", type_text "<password>", then click "#login-btn".
Expect: the message area shows "<error>", and the browser is still on the sign-in page.

### Examples
| username | password      | error                          |
|----------|---------------|---------------------------------|
|          | wonderland123 | Username is required.          |
| alice    |               | Password is required.          |
| alice    | wrongpassword | Invalid username or password.  |
