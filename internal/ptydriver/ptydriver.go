// Package ptydriver runs a shell inside a pseudo-terminal and gives the
// runner a simple snapshot-based API: write input, wait, read whatever new
// output has appeared. This mirrors how a human runs a manual test — type
// a command, look at the screen, decide the next move — which is also the
// interaction shape the SLM reasons over each turn.
package ptydriver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
)

// Driver owns one PTY-backed shell process for the lifetime of a test run.
type Driver struct {
	cmd *exec.Cmd
	f   *os.File

	mu  sync.Mutex
	buf bytes.Buffer // full session output, grows monotonically

	closed chan struct{}
}

// Start launches shell (e.g. "/bin/bash") attached to a new PTY.
func Start(shell string, env []string) (*Driver, error) {
	cmd := exec.Command(shell)
	if env != nil {
		cmd.Env = env
	}
	f, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("starting pty: %w", err)
	}
	// A sane default size; resize with pty.Setsize if the shell under
	// test cares about terminal width (e.g. progress bars, wide tables).
	_ = pty.Setsize(f, &pty.Winsize{Rows: 40, Cols: 200})

	d := &Driver{cmd: cmd, f: f, closed: make(chan struct{})}
	go d.pump()
	return d, nil
}

// pump continuously copies PTY output into the internal buffer. Run once
// in the background for the lifetime of the Driver.
func (d *Driver) pump() {
	chunk := make([]byte, 4096)
	for {
		n, err := d.f.Read(chunk)
		if n > 0 {
			d.mu.Lock()
			d.buf.Write(chunk[:n])
			d.mu.Unlock()
		}
		if err != nil {
			close(d.closed)
			return
		}
	}
}

// Write sends raw bytes to the shell's stdin (i.e. types into the PTY).
func (d *Driver) Write(s string) error {
	_, err := d.f.WriteString(s)
	return err
}

// RunCommand types command, presses Enter (unless pressEnter is false),
// waits waitFor, then returns everything the PTY has printed since the
// last snapshot (see SinceLastSnapshot).
func (d *Driver) RunCommand(ctx context.Context, command string, pressEnter bool, waitFor time.Duration) (string, error) {
	if pressEnter {
		command += "\n"
	}
	if err := d.Write(command); err != nil {
		return "", err
	}
	return d.WaitAndSnapshot(ctx, waitFor)
}

// WaitAndSnapshot blocks for waitFor (or until ctx is cancelled) and then
// returns new output since the last snapshot.
func (d *Driver) WaitAndSnapshot(ctx context.Context, waitFor time.Duration) (string, error) {
	select {
	case <-time.After(waitFor):
	case <-ctx.Done():
		return "", ctx.Err()
	case <-d.closed:
	}
	return d.SinceLastSnapshot(), nil
}

// lastSnapshotLen tracks how much of buf has already been shown to the
// model, so each turn only sees NEW output rather than replaying the
// whole session (which would blow the context window fast).
var _ = io.EOF // (keeps io import if pump() signature changes; harmless)

// SinceLastSnapshot returns output appended since the previous call and
// advances the snapshot marker. Call this, not the raw buffer, from the
// runner so every turn gets a clean "what's new" view.
func (d *Driver) SinceLastSnapshot() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.buf.String()
	d.buf.Reset()
	return out
}

// Alive reports whether the underlying shell process is still running.
func (d *Driver) Alive() bool {
	select {
	case <-d.closed:
		return false
	default:
		return true
	}
}

// Close terminates the shell and releases the PTY.
func (d *Driver) Close() error {
	_ = d.f.Close()
	if d.cmd.Process != nil {
		_ = d.cmd.Process.Kill()
	}
	return d.cmd.Wait()
}
