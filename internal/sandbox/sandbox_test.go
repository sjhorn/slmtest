package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/example/slmtest/internal/ptydriver"
)

func TestProfileShape(t *testing.T) {
	got, err := Config{}.Profile()
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}

	for _, want := range []string{
		"(version 1)",
		"(allow default)",
		"(deny file-write*)",
		`(literal "/dev/null")`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("profile is missing %q:\n%s", want, got)
		}
	}
	// Network is allowed by default: a test that installs a package or
	// curls a local service is the common case.
	if strings.Contains(got, "(deny network*)") {
		t.Errorf("profile denies network by default:\n%s", got)
	}
}

func TestProfileDenyNetwork(t *testing.T) {
	got, err := Config{DenyNetwork: true}.Profile()
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if !strings.Contains(got, "(deny network*)") {
		t.Errorf("profile does not deny network:\n%s", got)
	}
}

func TestProfileIncludesWritablePaths(t *testing.T) {
	dir := t.TempDir()
	got, err := Config{WritablePaths: []string{dir}}.Profile()
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	// t.TempDir lives under TMPDIR, which is itself symlinked on macOS,
	// so compare against the resolved form.
	if !strings.Contains(got, quote(resolveExisting(dir))) {
		t.Errorf("profile is missing the extra writable path %q:\n%s", dir, got)
	}
}

// Seatbelt matches resolved paths, so a profile naming "/tmp" on macOS
// permits nothing at all. This is the single easiest way to write a
// profile that looks right and silently does nothing.
func TestResolvePathsFollowsSymlinks(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("path layout is macOS-specific")
	}
	got, err := resolvePaths([]string{"/tmp", "/var/tmp"})
	if err != nil {
		t.Fatalf("resolvePaths: %v", err)
	}
	want := []string{"/private/tmp", "/private/var/tmp"}
	if len(got) != len(want) {
		t.Fatalf("resolvePaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("resolvePaths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolvePathsDeduplicates(t *testing.T) {
	got, err := resolvePaths([]string{"/tmp", "/tmp", "", "   "})
	if err != nil {
		t.Fatalf("resolvePaths: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("resolvePaths = %v, want a single entry", got)
	}
}

// A writable path that doesn't exist yet is legitimate — the test may be
// what creates it — so it must not fail the run.
func TestResolveExistingHandlesMissingPaths(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "not", "created", "yet")

	got := resolveExisting(missing)
	if !strings.HasSuffix(got, filepath.Join("not", "created", "yet")) {
		t.Errorf("resolveExisting(%q) = %q, want the missing tail preserved", missing, got)
	}
	// The existing prefix should still have been resolved.
	if !strings.HasPrefix(got, resolveExisting(base)) {
		t.Errorf("resolveExisting(%q) = %q, want it under the resolved base %q", missing, got, resolveExisting(base))
	}
}

func TestDefaultWritablePathsIncludesTMPDIR(t *testing.T) {
	t.Setenv("TMPDIR", "/var/folders/example")
	got := DefaultWritablePaths()

	var found bool
	for _, p := range got {
		if p == "/var/folders/example" {
			found = true
		}
	}
	// Without TMPDIR, mktemp fails on macOS: it points into /var/folders,
	// not /tmp.
	if !found {
		t.Errorf("DefaultWritablePaths = %v, want it to include TMPDIR", got)
	}
}

func TestQuoteEscapes(t *testing.T) {
	tests := map[string]string{
		`/tmp/plain`:      `"/tmp/plain"`,
		`/tmp/a"b`:        `"/tmp/a\"b"`,
		`/tmp/a\b`:        `"/tmp/a\\b"`,
		`/tmp/a"b\c`:      `"/tmp/a\"b\\c"`,
		`/tmp/with space`: `"/tmp/with space"`,
	}
	for in, want := range tests {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestArgvDisabled(t *testing.T) {
	got, err := Config{}.Argv()
	if err != nil {
		t.Fatalf("Argv: %v", err)
	}
	if got != nil {
		t.Errorf("Argv = %v, want nil when sandboxing is off", got)
	}
}

func TestArgvGenerated(t *testing.T) {
	if err := Available(); err != nil {
		t.Skipf("sandboxing unavailable: %v", err)
	}
	got, err := Config{Enabled: true}.Argv()
	if err != nil {
		t.Fatalf("Argv: %v", err)
	}
	if len(got) != 3 || got[0] != Program || got[1] != "-p" {
		t.Fatalf("Argv = %v, want [%s -p <profile>]", got, Program)
	}
	if !strings.Contains(got[2], "(deny file-write*)") {
		t.Errorf("inline profile looks wrong:\n%s", got[2])
	}
}

func TestArgvCustomProfile(t *testing.T) {
	if err := Available(); err != nil {
		t.Skipf("sandboxing unavailable: %v", err)
	}
	path := filepath.Join(t.TempDir(), "custom.sb")
	if err := os.WriteFile(path, []byte("(version 1)\n(allow default)\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := Config{Enabled: true, ProfilePath: path}.Argv()
	if err != nil {
		t.Fatalf("Argv: %v", err)
	}
	want := []string{Program, "-f", path}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("Argv = %v, want %v", got, want)
	}

	if _, err := (Config{Enabled: true, ProfilePath: filepath.Join(t.TempDir(), "missing.sb")}).Argv(); err == nil {
		t.Error("Argv succeeded with a missing profile, want error")
	}
}

func TestAvailableRejectsNonDarwin(t *testing.T) {
	err := Available()
	if runtime.GOOS == "darwin" {
		if err != nil {
			t.Errorf("Available() = %v on darwin, want nil", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("Available() = nil on %s, want an error", runtime.GOOS)
	}
	// The error has to point somewhere useful, since this is the whole
	// story for a Linux user.
	if !strings.Contains(err.Error(), "-exec-prefix") {
		t.Errorf("error = %q, want it to suggest the portable alternative", err)
	}
}

// The profile is only worth anything if a real shell running under it is
// actually confined, so this drives one through the PTY the harness uses.
func TestSandboxedShellIsConfined(t *testing.T) {
	if err := Available(); err != nil {
		t.Skipf("sandboxing unavailable: %v", err)
	}

	scratch := t.TempDir()
	argv, err := Config{Enabled: true, WritablePaths: []string{scratch}}.Argv()
	if err != nil {
		t.Fatalf("Argv: %v", err)
	}

	d, err := ptydriver.Start(append(argv, "/bin/sh"), nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	_, _ = d.WaitAndSnapshot(context.Background(), 400*time.Millisecond)

	run := func(cmd string) string {
		t.Helper()
		out, err := d.RunCommand(context.Background(), cmd, true, 600*time.Millisecond)
		if err != nil {
			t.Fatalf("RunCommand(%q): %v", cmd, err)
		}
		return out
	}

	// Denied: the user's home directory.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(home, "slmtest-sandbox-probe")
	t.Cleanup(func() { _ = os.Remove(probe) })
	if out := run("touch " + probe + " 2>&1; echo rc=$?"); !strings.Contains(out, "rc=1") {
		t.Errorf("writing to the home directory was not denied: %q", out)
	}
	if _, err := os.Stat(probe); err == nil {
		t.Errorf("%s was created despite the sandbox", probe)
	}

	// Allowed: the scratch dir passed in, /tmp, and /dev/null.
	if out := run("touch " + scratch + "/ok 2>&1; echo rc=$?"); !strings.Contains(out, "rc=0") {
		t.Errorf("writing to an explicitly writable path was denied: %q", out)
	}
	if out := run("touch /tmp/slmtest-sandbox-ok 2>&1; echo rc=$?"); !strings.Contains(out, "rc=0") {
		t.Errorf("writing to /tmp was denied: %q", out)
	}
	// A bare `deny file-write*` breaks every `>/dev/null` in every test,
	// so this one is load-bearing.
	if out := run("echo x > /dev/null 2>&1; echo rc=$?"); !strings.Contains(out, "rc=0") {
		t.Errorf("writing to /dev/null was denied: %q", out)
	}
	// mktemp needs TMPDIR (under /var/folders on macOS), not just /tmp.
	if out := run("mktemp -d >/dev/null 2>&1; echo rc=$?"); !strings.Contains(out, "rc=0") {
		t.Errorf("mktemp was denied: %q", out)
	}
	// Reads stay allowed — this confines writes, not visibility.
	if out := run("ls / >/dev/null 2>&1; echo rc=$?"); !strings.Contains(out, "rc=0") {
		t.Errorf("reading / was denied: %q", out)
	}
}
