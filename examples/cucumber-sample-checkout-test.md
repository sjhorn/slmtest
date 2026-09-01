---
name: cucumber-sample-checkout
description: "A real Cucumber .feature file, not authored for this project, translated into this markdown dialect and run against the real public site it targets — not a local fixture. Source: BaneleMlamleli/swaglabs_playwright, features/checkout-negative.feature (github.com/BaneleMlamleli/swaglabs_playwright), against saucedemo.com, a public Sauce Labs demo site built for exactly this kind of automation practice. Structure (Background, a tagged Scenario Outline, an Examples table) is an unmodified transcription of the original file's shape; step wording is lightly adapted from Gherkin's Given/When/Then/And into this format's Goal/Hint/Expect fields. Requires a build with `-tags browserdriver` and `-driver-option url=https://www.saucedemo.com/`."
driver: browser
timeout_seconds: 600
max_turns_per_step: 10
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
### Step 1: When I fill in the checkout information with first name "<firstName>", last name "<lastName>" and postal code "<postalCode>", and click continue
Goal: this is a NEGATIVE test — the checkout form correctly REJECTS this submission and shows a validation error. Seeing that error IS the pass condition.
Hint: click "#first-name", type_text "<firstName>", click "#last-name", type_text "<lastName>", click "#postal-code", type_text "<postalCode>", then click "#continue". IMPORTANT: the very next turn after you see any text starting with "Error:" appear on the page, your ONLY allowed action is finish_step with step_result "pass" — not another click, not another type_text. Seeing the error text is the entire goal of this step.
Expect: the page's visible text contains the error message "<errorMessage>". The moment that text is visible, immediately call finish_step and pass — do not attempt to correct the form afterward, that is not part of this step.

### Examples
| firstName | lastName | postalCode | errorMessage                   |
|-----------|----------|------------|---------------------------------|
|           | Doe      | 12345      | Error: First Name is required  |
| John      |          | 12345      | Error: Last Name is required   |
| John      | Doe      |            | Error: Postal Code is required |
|           |          |            | Error: First Name is required  |
