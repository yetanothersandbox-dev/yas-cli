package main

import (
	"reflect"
	"strings"
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

// `yas new api` must name the box `api`.
//
// The positional form is what everybody types and what the README and the
// website have always shown. Until it was wired up the argument was IGNORED:
// the box got a generated name, the user got a working box under a name they
// did not choose, and nothing said so. A silently-different result is worse
// than an error.
func TestNewTakesAPositionalName(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
		err  string
	}{
		{"positional", []string{"api"}, "api", ""},
		{"flag", []string{"-name", "api"}, "api", ""},
		{"both agreeing", []string{"-name", "api", "api"}, "api", ""},
		{"neither", []string{}, "", ""},
		{"both disagreeing", []string{"-name", "api", "web"}, "", "two different names"},
		{"too many", []string{"api", "web"}, "", "one name at most"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsName := ""
			var positional []string
			for i := 0; i < len(tc.args); i++ {
				if tc.args[i] == "-name" && i+1 < len(tc.args) {
					fsName = tc.args[i+1]
					i++
					continue
				}
				positional = append(positional, tc.args[i])
			}
			got, err := reconcileName(fsName, positional)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want one containing %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("name = %q, want %q", got, tc.want)
			}
		})
	}
}
