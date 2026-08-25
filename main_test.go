package main

import (
	"reflect"
	"testing"
)

// The reserved-verb set versus passthrough: what dispatches where, and — the
// property that matters — that a passthrough command's own flags survive
// untouched from its first token on.
func TestSplitPassthrough(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantBox  string
		wantNew  bool
		wantRest []string
		wantErr  bool
	}{
		{
			name:     "claude keeps its scary flag",
			args:     []string{"claude", "--dangerously-skip-permissions"},
			wantRest: []string{"claude", "--dangerously-skip-permissions"},
		},
		{
			name:     "box flag peels off the front",
			args:     []string{"-b", "brisk-otter-4f2a", "claude", "-p", "hi"},
			wantBox:  "brisk-otter-4f2a",
			wantRest: []string{"claude", "-p", "hi"},
		},
		{
			name:     "new flag composes",
			args:     []string{"--new", "htop"},
			wantNew:  true,
			wantRest: []string{"htop"},
		},
		{
			name:     "a -b AFTER the command belongs to the command",
			args:     []string{"sort", "-b", "file"},
			wantRest: []string{"sort", "-b", "file"},
		},
		{
			name:    "b with no id is an error",
			args:    []string{"-b"},
			wantErr: true,
		},
		{
			name:    "only flags, nothing to run",
			args:    []string{"--new"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			box, fresh, rest, err := splitPassthrough(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatal("no error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if box != tc.wantBox || fresh != tc.wantNew || !reflect.DeepEqual(rest, tc.wantRest) {
				t.Fatalf("got (%q, %v, %v), want (%q, %v, %v)", box, fresh, rest, tc.wantBox, tc.wantNew, tc.wantRest)
			}
		})
	}
}

// Every verb the usage text promises is dispatchable, and the verb set stays
// deliberately small — each entry shadows a program of the same name inside
// every box.
func TestVerbTableCoversTheUsageText(t *testing.T) {
	for _, v := range []string{"new", "list", "ls", "ssh", "connect", "rm", "suspend", "resume", "exec", "login", "stdio"} {
		if _, ok := verbs[v]; !ok {
			t.Errorf("verb %q is in the usage text and not in the table", v)
		}
	}
	if _, ok := verbs["claude"]; ok {
		t.Error("claude must NOT be a verb; it is the canonical passthrough")
	}
}
