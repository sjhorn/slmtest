package ptydriver

import (
	"context"
	"strings"
	"testing"
	"time"
)

// These tests drive a real /bin/sh through a real PTY — the whole point of
// the package is the interaction with a live terminal, so mocking it out
// would leave the interesting behavior untested.

func startTestDriver(t *testing.T) *Driver {
	t.Helper()
	d, err := Start("/bin/sh", nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	// Drain the shell's startup banner/prompt so tests only see their own
	// command output.
	_, _ = d.WaitAndSnapshot(context.Background(), 300*time.Millisecond)
	return d
}

func TestRunCommandCapturesOutput(t *testing.T) {
	d := startTestDriver(t)

	out, err := d.RunCommand(context.Background(), "echo hello-from-pty", true, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if !strings.Contains(out, "hello-from-pty") {
		t.Errorf("output = %q, want it to contain hello-from-pty", out)
	}
}

// Each turn must see only NEW output — replaying the whole session every
// turn would blow the model's context window within a few commands.
func TestSnapshotReturnsOnlyNewOutput(t *testing.T) {
	d := startTestDriver(t)

	first, err := d.RunCommand(context.Background(), "echo marker-one", true, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if !strings.Contains(first, "marker-one") {
		t.Fatalf("first output = %q, want marker-one", first)
	}

	second, err := d.RunCommand(context.Background(), "echo marker-two", true, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if !strings.Contains(second, "marker-two") {
		t.Errorf("second output = %q, want marker-two", second)
	}
	if strings.Contains(second, "marker-one") {
		t.Errorf("second output replayed earlier output: %q", second)
	}
}

// send_keys semantics: without Enter the shell should not execute yet.
func TestWriteWithoutEnterDoesNotExecute(t *testing.T) {
	d := startTestDriver(t)

	out, err := d.RunCommand(context.Background(), "echo not-yet-executed", false, 400*time.Millisecond)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	// The typed text is echoed back by the terminal, but the marker must
	// not appear a second time as command *output*.
	if strings.Count(out, "not-yet-executed") > 1 {
		t.Errorf("command appears to have run without Enter: %q", out)
	}

	out, err = d.RunCommand(context.Background(), "", true, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("RunCommand (bare Enter): %v", err)
	}
	if !strings.Contains(out, "not-yet-executed") {
		t.Errorf("pressing Enter did not execute the buffered line; output = %q", out)
	}
}

func TestExitCodeIsVisibleToTheModel(t *testing.T) {
	d := startTestDriver(t)

	// The harness never infers pass/fail from exit codes itself, but it
	// must be possible for the model to ask for one and see it.
	out, err := d.RunCommand(context.Background(), "false; echo exit=$?", true, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if !strings.Contains(out, "exit=1") {
		t.Errorf("output = %q, want it to contain exit=1", out)
	}
}

func TestAliveFlipsWhenShellExits(t *testing.T) {
	d := startTestDriver(t)

	if !d.Alive() {
		t.Fatal("Alive() = false immediately after Start")
	}

	if _, err := d.RunCommand(context.Background(), "exit 0", true, 500*time.Millisecond); err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	// Give the pump goroutine a moment to observe EOF on the PTY.
	deadline := time.Now().Add(3 * time.Second)
	for d.Alive() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if d.Alive() {
		t.Error("Alive() = true after the shell exited")
	}
}

// A cancelled context must unblock the wait rather than stranding the run
// for the full duration — this is how the whole-test timeout takes effect.
func TestWaitAndSnapshotHonorsContextCancellation(t *testing.T) {
	d := startTestDriver(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := d.WaitAndSnapshot(ctx, 10*time.Second)
	if err == nil {
		t.Fatal("WaitAndSnapshot returned nil error for a cancelled context")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("WaitAndSnapshot blocked for %v despite cancellation", elapsed)
	}
}

func TestStartRejectsMissingShell(t *testing.T) {
	if _, err := Start("/nonexistent/shell/binary", nil); err == nil {
		t.Fatal("Start succeeded for a nonexistent shell, want error")
	}
}
