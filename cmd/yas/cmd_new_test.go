package main

import (
	"testing"
)

// parseVcpus is the CLI's only unit arithmetic: it turns the human spelling
// into the milli-vCPU the API sells in. A wrong conversion here is a box a
// thousand times bigger or smaller than the one the user asked for.
func TestParseVcpus(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"", 0, false}, // server default, like every size flag
		{"2", 2000, false},
		{"0.5", 500, false},
		{".5", 500, false},
		{"1.25", 1250, false},
		{"0.001", 1, false},
		{"  3 ", 3000, false},
		// Refused, not rounded: a fourth decimal is a number the milli unit
		// cannot carry, and rounding would bill something never asked for.
		{"0.0005", 0, true},
		{"0", 0, true}, // a box with no CPU is not a box
		{"-1", 0, true},
		{"two", 0, true},
		{"1.x", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseVcpus(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseVcpus(%q) = %d, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVcpus(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("parseVcpus(%q) = %d milli, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// The lists reach the wire whole. A flag the CLI accepts and then drops is a
// posture the user was told they had.
func TestBuildPolicyCarriesEveryList(t *testing.T) {
	p, err := buildPolicy(policyFlags{
		preset: "filtered", allow: "github.com, *.github.com ,", deny: "gist.github.com",
		allowNets: "203.0.113.0/24:443", denyNets: "203.0.113.66",
	})
	if err != nil {
		t.Fatal(err)
	}
	e := p.Egress
	// The trailing comma is dropped rather than sent as an empty rule the
	// server then has to refuse.
	if len(e.Allow) != 2 || e.Allow[0] != "github.com" || e.Allow[1] != "*.github.com" {
		t.Fatalf("allow = %v", e.Allow)
	}
	if len(e.Deny) != 1 || len(e.AllowNets) != 1 || len(e.DenyNets) != 1 {
		t.Fatalf("egress = %+v", e)
	}
}
