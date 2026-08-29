package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   \t ", nil},
		{"single word", "docker", []string{"docker"}},
		{
			name: "the common case",
			in:   "docker run --rm -it ubuntu:24.04",
			want: []string{"docker", "run", "--rm", "-it", "ubuntu:24.04"},
		},
		{"collapses runs of spaces", "a   b\t\tc", []string{"a", "b", "c"}},
		{"leading and trailing space", "  a b  ", []string{"a", "b"}},
		{
			name: "double quotes group",
			in:   `docker run -e "GREETING=hello world" img`,
			want: []string{"docker", "run", "-e", "GREETING=hello world", "img"},
		},
		{
			name: "single quotes group",
			in:   `sh -c 'echo one two'`,
			want: []string{"sh", "-c", "echo one two"},
		},
		{
			name: "quotes inside a word",
			in:   `-e"K=V W"`,
			want: []string{"-eK=V W"},
		},
		{
			name: "an empty quoted string is still an argument",
			in:   `a "" b`,
			want: []string{"a", "", "b"},
		},
		{
			name: "backslash escapes a space",
			in:   `apptainer exec /my\ images/img.sif`,
			want: []string{"apptainer", "exec", "/my images/img.sif"},
		},
		{
			name: "backslash escapes a quote",
			in:   `echo \"quoted\"`,
			want: []string{"echo", `"quoted"`},
		},
		{
			name: "double quotes preserve single quotes",
			in:   `-c "it's fine"`,
			want: []string{"-c", "it's fine"},
		},
		{
			// Inside single quotes a backslash is literal, matching sh.
			name: "backslash is literal inside single quotes",
			in:   `'a\b'`,
			want: []string{`a\b`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := splitArgs(tc.in)
			if err != nil {
				t.Fatalf("splitArgs(%q): %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitArgs(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestSplitArgsErrors(t *testing.T) {
	tests := []struct {
		in      string
		wantErr string
	}{
		{`docker run "unterminated`, "unterminated"},
		{`docker run 'unterminated`, "unterminated"},
		{`trailing\`, "trailing backslash"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if _, err := splitArgs(tc.in); err == nil {
				t.Fatalf("splitArgs(%q) succeeded, want error", tc.in)
			} else if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestTakeLeadingPositional(t *testing.T) {
	got, rest, err := takeLeadingPositional([]string{"test.md", "-verbose", "-json"})
	if err != nil {
		t.Fatalf("takeLeadingPositional: %v", err)
	}
	if got != "test.md" {
		t.Errorf("positional = %q, want test.md", got)
	}
	// Flags after the path must survive for FlagSet.Parse — this is the
	// whole reason the helper exists.
	if !reflect.DeepEqual(rest, []string{"-verbose", "-json"}) {
		t.Errorf("rest = %#v", rest)
	}

	for _, args := range [][]string{nil, {}, {"-verbose", "test.md"}, {""}} {
		if _, _, err := takeLeadingPositional(args); err == nil {
			t.Errorf("takeLeadingPositional(%#v) succeeded, want error", args)
		}
	}
}
