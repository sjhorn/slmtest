package main

import (
	"runtime"
	"testing"

	"github.com/sjhorn/slmtest/internal/cliops"
)

func TestSandboxParamsToConfigNilDefaultsOSAware(t *testing.T) {
	var s *SandboxParams
	got := s.toConfig()
	want := cliops.DefaultSandboxEnabled(runtime.GOOS)
	if got.Enabled != want {
		t.Errorf("nil Sandbox param: Enabled = %v, want %v (OS-aware default)", got.Enabled, want)
	}
}

func TestSandboxParamsToConfigExplicitEmptyObjectDisables(t *testing.T) {
	// A caller sending any sandbox object at all, even {}, is explicit
	// and gets exactly the Enabled value it specified (default false
	// within that object) regardless of OS.
	got := (&SandboxParams{}).toConfig()
	if got.Enabled {
		t.Errorf("explicit empty Sandbox param: Enabled = true, want false regardless of OS")
	}
}

func TestSandboxParamsToConfigExplicitEnabledRespected(t *testing.T) {
	got := (&SandboxParams{Enabled: true, DenyNetwork: true}).toConfig()
	if !got.Enabled || !got.DenyNetwork {
		t.Errorf("got %+v, want Enabled and DenyNetwork both true", got)
	}
}
