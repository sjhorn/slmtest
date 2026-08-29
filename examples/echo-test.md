---
name: echo-smoke-test
description: Trivial test used to smoke-test the harness itself (no real SLM needed)
shell: /bin/bash
timeout_seconds: 60
max_turns_per_step: 4
---

## Step 1: Echo a marker string
Goal: the shell can execute a command and return output.
Hint: echo hello-from-pty
Expect: output contains "hello-from-pty".
