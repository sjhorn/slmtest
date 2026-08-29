---
name: nginx-smoke-test
description: Verify nginx installs, starts, and serves the default page
shell: /bin/bash
timeout_seconds: 300
max_turns_per_step: 8
---

## Step 1: Install nginx
Goal: nginx is installed and the binary is on PATH.
Hint: apt-get update && apt-get install -y nginx
Expect: `nginx -v` exits 0 and prints a version string like "nginx version: nginx/...".

## Step 2: Start the nginx service
Goal: the nginx service is running and listening on port 80.
Hint: service nginx start
Expect: `service nginx status` (or `curl -s -o /dev/null -w '%{http_code}' http://localhost:80`) shows it running / returns HTTP 200.

## Step 3: Serve the default page
Goal: requesting the default site returns the stock nginx welcome page.
Hint: curl -s http://localhost:80
Expect: the response body contains the text "Welcome to nginx".

## Step 4: Config file is in the expected location
Goal: the main config file exists where nginx expects it.
Hint: test -f /etc/nginx/nginx.conf && echo FOUND
Expect: output contains "FOUND".
