package ui

import (
	"bytes"
	"strings"
	"testing"
)

// A refusal in a pipe stays the single line scripts have always seen.
//
// This is the compatibility half of the redesign. CI logs are grepped, and a
// three-line block with a box-drawing arrow in the middle is not what anything
// parsing `yas:` expects. The words may improve; the shape may not.
func TestARefusalInAPipeIsOneGreppableLine(t *testing.T) {
	for _, tc := range []struct {
		name, slug, message string
		wantContains        []string
	}{
		{"a known slug", "quota_exceeded", "your pool has 0.5 GiB free",
			[]string{"yas: ", "your pool has 0.5 GiB free", "next: yas suspend"}},
		{"an unknown slug", "something_new", "the server said this",
			[]string{"yas: the server said this"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			Refusal(&b, tc.slug, tc.message, false)
			got := b.String()
			if n := strings.Count(strings.TrimRight(got, "\n"), "\n"); n != 0 {
				t.Errorf("a piped refusal spans %d extra lines:\n%s", n, got)
			}
			if strings.Contains(got, "\x1b[") {
				t.Errorf("a piped refusal carries ANSI escapes: %q", got)
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("piped refusal = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}

// An unknown slug gets the server's words and NO advice.
//
// Inventing a next step for a failure this CLI has never seen sends somebody
// somewhere useless with the tool's full confidence behind it.
func TestAnUnknownRefusalInventsNoAdvice(t *testing.T) {
	var b bytes.Buffer
	Refusal(&b, "a_slug_from_the_future", "something went wrong upstream", true)
	got := b.String()
	if strings.Contains(got, Arrow) {
		t.Errorf("an unknown slug was given advice:\n%s", got)
	}
	if !strings.Contains(got, "something went wrong upstream") {
		t.Errorf("the server's own message was dropped: %q", got)
	}
}

// The server's message is never paraphrased away.
//
// It is the only part that knows the actual numbers — how much pool is free,
// which limit was hit — so a CLI that replaced it with its own headline would
// be throwing away the only specific thing in the refusal.
func TestAKnownRefusalKeepsTheServersOwnSentence(t *testing.T) {
	var b bytes.Buffer
	Refusal(&b, "quota_exceeded", "0.5 GiB free and 4.0 GiB wanted", true)
	got := b.String()
	if !strings.Contains(got, "0.5 GiB free and 4.0 GiB wanted") {
		t.Errorf("the server's numbers were dropped in favour of the headline:\n%s", got)
	}
	if !strings.Contains(got, "your pool is full") {
		t.Errorf("the headline is missing:\n%s", got)
	}
}
