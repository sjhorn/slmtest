package buildinfo

import "testing"

func TestGetDoesNotPanicAndReportsAVersion(t *testing.T) {
	info := Get()
	if info.Version == "" {
		t.Error("Version = \"\", want a non-empty version (falls back to \"dev\")")
	}
}
