//go:build browserdriver

package main

// Registers the "browser" driver — only compiled in with
// `go build -tags browserdriver`, so the default build has no
// Playwright/browser-binary dependency. See internal/browserdriver's
// package doc comment for why.
import _ "github.com/sjhorn/slmtest/internal/browserdriver"
