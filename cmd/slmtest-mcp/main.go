// Command slmtest-mcp is an MCP (Model Context Protocol) server exposing
// slmtest's run/validate/init as structured tools over stdio, for an
// agent (e.g. Claude Code) that wants a discoverable, typed interface
// instead of shelling out to the slmtest CLI and parsing -json output.
//
// It is a separate binary from cmd/slmtest, not a mode flag on it: an
// MCP server is a long-running stdio JSON-RPC process with a different
// lifecycle than a one-shot CLI invocation. Both import the same
// internal/runner/internal/spec/internal/agent/internal/driver packages
// via internal/cliops's shared, flag-independent helpers, so there is no
// logic duplication — only a different outer transport loop.
package main

import (
	"context"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	_ "github.com/sjhorn/slmtest/internal/nulldriver" // registers the "null" driver
	_ "github.com/sjhorn/slmtest/internal/ptydriver"  // registers the "tui" driver
)

// version is deliberately static rather than tied to VCS info — this
// server has no release process yet, and a stale-looking version string
// is less confusing than a build-time-injected one nobody wires up.
const version = "0.1.0"

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "slmtest", Version: version}, &mcp.ServerOptions{
		Instructions: "Runs markdown-defined terminal tests driven by a small language model. " +
			"Use validate_test to parse-check a spec while authoring it (fast, no model call), " +
			"then run_test to execute it against an SLM endpoint.",
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "run_test",
		Description: "Run a markdown slmtest spec end-to-end against an SLM endpoint and return the same " +
			"structured report `slmtest run -json` produces (steps, verdicts, reasons, transcript).",
	}, handleRunTest)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "validate_test",
		Description: "Parse-check a markdown slmtest spec: no execution, no model call. Safe to call liberally while authoring a spec.",
	}, handleValidateTest)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "init_test",
		Description: "Write a starter markdown slmtest spec template to a new file. Refuses to overwrite an existing file.",
	}, handleInitTest)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Println("slmtest-mcp:", err)
		os.Exit(1)
	}
}
