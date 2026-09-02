---
name: cucumber-sample-checkout-split
description: "A mitigated variant of cucumber-sample-checkout-test.md, addressing the real finding documented there: the model correctly triggers the validation error, then tries to 'fix' the form instead of recognizing the error as success — unmoved by three prompt-wording iterations in the original, and unmoved by a higher-temperature retry either (see docs/model-runs.md). This version restructures the Scenario Outline's one combined step into three, matching the ORIGINAL .feature file's own When/And/Then line boundaries more faithfully than the single-step version did (that collapsing was this project's own translation choice, not something in the source file). Each line becomes its own step, so the assertion step starts with a fresh per-step history and the error already sitting in front of it — nothing left to 'fix'. Verified: the assertion step passed cleanly in all 4 scenarios across 3 separate runs (12/12); a smaller, unrelated turn-budget/efficiency issue (occasional 'type_text with no text key' stumbles on intentionally-blank fields) accounts for the rest of what's short of a clean 4/4 run — see docs/model-runs.md for the full account. Requires a build with `-tags browserdriver` and `-driver-option url=https://www.saucedemo.com/`."
driver: browser
timeout_seconds: 600
max_turns_per_step: 12
---

## Background
### Step 1: Given I am logged in as a standard user
Goal: logged in to Swag Labs as the standard user.
Hint: click "#user-name", type_text "standard_user", click "#password", type_text "secret_sauce", then click "#login-button".
Expect: the page has navigated to the inventory page, showing "Products" and a list of items each with an "Add to cart" button.

### Step 2: And I have added a product to the cart
Goal: one product is in the cart.
Hint: click "#add-to-cart-sauce-labs-backpack".
Expect: that button's own label now reads "Remove" instead of "Add to cart".

### Step 3: And I go to the cart page
Goal: the cart page is shown.
Hint: click the shopping cart icon (its selector should be listed among the interactive elements, something like "a.shopping_cart_link").
Expect: the page has navigated to the cart page, showing "Your Cart" and the item added in the previous step.

### Step 4: And I proceed to checkout
Goal: the checkout information page is shown.
Hint: click "#checkout".
Expect: the page has navigated to the checkout information page, showing First Name, Last Name, and Zip/Postal Code fields.

@checkout @negative
## Scenario Outline: Checkout is blocked when a required field is missing
### Step 1: When I fill in the checkout information with first name "<firstName>", last name "<lastName>" and postal code "<postalCode>"
Goal: the three checkout fields hold the given values — a blank value in the table means that field is intentionally left empty. This step is purely data entry; it does not judge whether checkout succeeds or fails.
Hint: click "#first-name", type_text "<firstName>", click "#last-name", type_text "<lastName>", click "#postal-code", type_text "<postalCode>".
Expect: the First Name, Last Name, and Zip/Postal Code fields hold the given values (an intentionally blank one stays empty) — checkable from the "Interactive elements" list, which shows each field's current value as its label.

### Step 2: And I click continue on the checkout information page
Goal: the Continue button is clicked.
Hint: click "#continue".
Expect: the page has responded to clicking Continue in some way — new text appeared, or the page changed. This step only confirms the click registered, not what the response means.

### Step 3: Then I should see the checkout error message "<errorMessage>"
Goal: the specific validation error from submitting incomplete information is visible.
Expect: the page's visible text contains the error message "<errorMessage>".

### Examples
| firstName | lastName | postalCode | errorMessage                   |
|-----------|----------|------------|---------------------------------|
|           | Doe      | 12345      | Error: First Name is required  |
| John      |          | 12345      | Error: Last Name is required   |
| John      | Doe      |            | Error: Postal Code is required |
|           |          |            | Error: First Name is required  |
