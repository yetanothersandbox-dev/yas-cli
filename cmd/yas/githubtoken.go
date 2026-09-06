package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
	"github.com/yetanothersandbox-dev/yas-cli/internal/cliio"
	"github.com/yetanothersandbox-dev/yas-cli/internal/config"
)

// `yas login -github`: a GitHub API token, stored on the account.
//
// # Why this is a SECOND GitHub credential and not the one you already have
//
// Signing in with GitHub leaves a token in custody already, and boxes use it —
// but only through the built-in `/ghapi` route, which is an ALLOWLIST of
// `/repos/{owner}/{name}/...` paths. There is no search endpoint on it, no
// `/orgs`, no `/user/repos`. So "list every PR I opened across an org" is not a
// thing that route can express, whatever token is behind it.
//
// It is also a GitHub APP token — issued to this product's App, reaching only
// repositories that App is installed on. Your own account's reach is wider than
// the App's almost everywhere it matters.
//
// The integration is the wider door: the whole REST API, at the token's own
// grant. The two are not interchangeable and neither replaces the other, which
// is why storing this does not touch the sign-in token.
//
// # Where the token comes from
//
// From the `gh` CLI when it is signed in, because it is already there and its
// token is already the right shape. The command run is HARD-CODED HERE and is
// not something the server can influence — a catalogue that could name a
// command to run on a laptop would be a remote code execution with extra steps.

// ghTokenCommand is the local pickup, spelled out in one place. Client-side and
// constant, deliberately: see the note above.
var ghTokenCommand = []string{"gh", "auth", "token"}

// ghStatusCommand reports who `gh` is and what its token may do. Its output is
// shown before anything is stored, because the scopes ARE the blast radius and
// a person handing one over should see it rather than be told about it.
var ghStatusCommand = []string{"gh", "auth", "status"}

// githubIntegrationID is the name this integration is always stored under.
//
// Fixed rather than chosen, so `yas login -github` is idempotent and so the
// hostname a box reaches it on (`github.<integration domain>`) is the same on
// every account — which is what lets a prompt name it without being told.
const githubIntegrationID = "github"

// localGitHubToken returns the `gh` CLI's own token, and whether it was there.
//
// A failure is not an error: `gh` may be absent, or present and signed out, and
// both simply mean "ask the human to paste one instead".
func localGitHubToken() (string, bool) {
	path, err := exec.LookPath(ghTokenCommand[0])
	if err != nil {
		return "", false
	}
	out, err := exec.Command(path, ghTokenCommand[1:]...).Output()
	if err != nil {
		return "", false
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", false
	}
	return tok, true
}

// localGitHubStatus is `gh auth status`, or "" if it cannot be read.
//
// gh prints the token REDACTED here (`gho_****`) and the scopes in full, which
// is exactly the split we want to show.
func localGitHubStatus() string {
	path, err := exec.LookPath(ghStatusCommand[0])
	if err != nil {
		return ""
	}
	// gh writes this to stderr on some versions and stdout on others, so both
	// are taken. CombinedOutput also means a non-zero exit still yields the
	// message, which is the case worth showing.
	out, _ := exec.Command(path, ghStatusCommand[1:]...).CombinedOutput()
	return strings.TrimSpace(string(out))
}

// storeGitHubIntegration is `yas login -github`.
//
// attach is the attachment spec to write, or "" to keep whatever the existing
// integration had — so re-running this to rotate a token cannot silently widen
// where it applies.
func storeGitHubIntegration(cfg config.Config, attach string) error {
	key := cfg.APIKeyResolved()
	if key == "" {
		return errors.New("no API key yet; run `yas login` first — a GitHub token is stored on your account")
	}
	base := cfg.BaseURLResolved()
	if base == "" {
		base = api.DefaultBaseURL
	}
	cl := &api.Client{BaseURL: base, Key: key}
	ctx := context.Background()

	// What is there already, so a rotation keeps its attachment and a first run
	// can tell the difference.
	existing, err := cl.Integration(ctx, githubIntegrationID)
	isNew := err != nil && api.IsNotFound(err)
	if err != nil && !isNew {
		return err
	}

	token, fromGH := localGitHubToken()
	if fromGH {
		fmt.Fprintln(os.Stderr, "Taking the token from the GitHub CLI on this machine:")
		if st := localGitHubStatus(); st != "" {
			fmt.Fprintln(os.Stderr, indentLines(st, "  "))
		}
		// Said before it is stored, not after. The scopes above are what
		// anything in a box can SPEND through the proxy — the token itself
		// never enters a guest, but a `repo`-scoped one can write to every
		// repository it grants, and that is a thing to notice now.
		fmt.Fprintln(os.Stderr, "\nThose scopes are what a box can spend through the proxy. The token itself never")
		fmt.Fprintln(os.Stderr, "enters a guest — the host attaches it per request — but anything running in one")
		fmt.Fprintln(os.Stderr, "can use it. Narrow it later with `yas integrations add github --methods GET`.")
		if cliio.IsTTY(os.Stdin) && !confirm("\nStore this token? [y/N] ") {
			return errors.New("not stored")
		}
	} else {
		fmt.Fprintln(os.Stderr, "No signed-in GitHub CLI found on this machine.")
		fmt.Fprintln(os.Stderr, "Paste a personal access token instead — classic with `repo`, or fine-grained.")
		token, err = promptSecret("GitHub token: ")
		if err != nil {
			return err
		}
	}

	req := api.IntegrationRequest{
		Service:     githubIntegrationID,
		Description: "GitHub REST API, at this token's own grant",
		Secret:      &token,
	}
	switch {
	case attach != "":
		req.Attach = splitComma(attach)
	case !isNew && len(existing.Attach) > 0:
		// A rotation keeps where it applied. Re-running to replace an expired
		// token must not quietly hand it to every box because the flag was left
		// off the second time.
		req.Attach = existing.Attach
	default:
		req.Attach = []string{"all"}
	}

	in, err := cl.PutIntegration(ctx, githubIntegrationID, req)
	if err != nil {
		return fmt.Errorf("the gateway refused to store it: %w", err)
	}

	verb := "stored"
	if !isNew {
		verb = "replaced"
	}
	fmt.Fprintf(os.Stderr, "\n✓ GitHub token %s, sealed on your account — applies to %s\n",
		verb, strings.Join(in.Attach, ", "))
	fmt.Fprintf(os.Stderr, "  A box reaches it at http://%s.int.yetanothersandbox.dev/ — the whole REST API,\n", githubIntegrationID)
	fmt.Fprintln(os.Stderr, "  including /search/issues, which the built-in /ghapi route cannot do.")
	fmt.Fprintln(os.Stderr, "  This is separate from the token `yas login` stores; neither replaces the other.")
	return nil
}

// indentLines prefixes every line, so a borrowed tool's output is visibly
// quoted rather than passed off as ours.
func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// confirm asks a yes/no question on the terminal. Anything but an explicit yes
// is no — this one stores a credential.
func confirm(prompt string) bool {
	fmt.Fprint(os.Stderr, prompt)
	var answer string
	if _, err := fmt.Fscanln(os.Stdin, &answer); err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	}
	return false
}
