//go:build browserdriver

package main

// Registers the "browser" driver — only compiled in with
// `go build -tags browserdriver`, mirroring cmd/slmtest's own opt-in
// registration. See internal/browserdriver's package doc comment.
import _ "github.com/sjhorn/slmtest/internal/browserdriver"
