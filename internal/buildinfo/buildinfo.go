// Package buildinfo reports version/VCS metadata about the running
// slmtest binary, for the audit-trail RunContext fields (see
// docs/roadmap-reporting-and-agents.md, Phase A). It uses only
// runtime/debug.ReadBuildInfo() — no exec.Command("git", ...) — so it
// works identically in a machine with no git installed, since the Go
// toolchain embeds this at build time.
package buildinfo

import "runtime/debug"

// Info is version/VCS metadata about the current binary.
type Info struct {
	// Version is the main module's version, e.g. "(devel)" for a `go
	// build`/`go run` invocation with no tagged release, or "dev" when
	// build info isn't available at all (not a module build).
	Version string
	// GitCommit is the VCS revision the binary was built from, when the
	// toolchain recorded one (a git checkout; empty for `go run` outside
	// a repo, or a binary built from a module cache/tarball with no VCS
	// info attached).
	GitCommit string
	// GitDirty reports whether the working tree had uncommitted changes
	// at build time, per the toolchain's own "vcs.modified" setting.
	GitDirty bool
}

// Get reads the current binary's build info. Safe to call unconditionally
// — it never panics, even when debug.ReadBuildInfo reports ok=false (e.g.
// a binary built without module mode).
func Get() Info {
	info := Info{Version: "dev"}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	if bi.Main.Version != "" {
		info.Version = bi.Main.Version
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.GitCommit = s.Value
		case "vcs.modified":
			info.GitDirty = s.Value == "true"
		}
	}
	return info
}
