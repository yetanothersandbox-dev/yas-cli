package main

import (
	"strings"
	"testing"
)

// The local pickup is HARD-CODED and client-side. A catalogue entry that could
// name a command to run on somebody's laptop would be remote code execution
// with extra steps, so this pins that the command is a constant and that it is
// the one we mean.
func TestGitHubTokenPickupIsAFixedLocalCommand(t *testing.T) {
	if got := strings.Join(ghTokenCommand, " "); got != "gh auth token" {
		t.Fatalf("pickup command = %q, want the gh CLI's own token command", got)
	}
	if got := strings.Join(ghStatusCommand, " "); got != "gh auth status" {
		t.Fatalf("status command = %q", got)
	}
	// Nothing in either may be taken from a server response. If this ever
	// becomes a variable, that decision needs making on purpose.
	for _, c := range [][]string{ghTokenCommand, ghStatusCommand} {
		for _, arg := range c {
			if strings.ContainsAny(arg, "$`;|&><") {
				t.Fatalf("argument %q looks like shell, and these are exec'd without one", arg)
			}
		}
	}
}

// The integration name is fixed so the hostname a box reaches it on is the same
// on every account — which is what lets a prompt name it without being told.
func TestGitHubIntegrationIDIsStable(t *testing.T) {
	if githubIntegrationID != "github" {
		t.Fatalf("id = %q; a prompt that names github.<domain> would stop resolving", githubIntegrationID)
	}
}

func TestIndentLines(t *testing.T) {
	got := indentLines("a\nb", "  ")
	if got != "  a\n  b" {
		t.Fatalf("got %q", got)
	}
	// A single line still gets the prefix: borrowed output is always visibly
	// quoted rather than passed off as ours.
	if got := indentLines("only", "> "); got != "> only" {
		t.Fatalf("got %q", got)
	}
}
