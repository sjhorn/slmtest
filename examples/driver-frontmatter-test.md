---
name: driver-frontmatter-test
description: Same as echo-test.md, but selects the driver explicitly via the driver:/tui_* frontmatter fields instead of the deprecated unprefixed shell:/term:/size: keys
driver: tui
tui_shell: /bin/bash
timeout_seconds: 60
max_turns_per_step: 4
---

## Step 1: Echo a marker string
Goal: the shell can execute a command and return output.
Hint: echo hello-from-pty
Expect: output contains "hello-from-pty".
