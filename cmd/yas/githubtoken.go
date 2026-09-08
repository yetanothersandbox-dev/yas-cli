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
// # What storing one REPLACES
//
// Signing in with GitHub already leaves a token in custody, and boxes use it.
// It is a GitHub APP token, issued to this product's App, so it reaches only
// repositories that App is installed on — which is an org-admin action nobody
// may have taken. Your own account's reach is wider than the App's almost
// everywhere it matters.
//
// This used to be described as a SECOND, wider door, and that was true of the
// code at the time and was the bug. A box had two GitHub credentials and more
// than one way to reach GitHub, and which one answered depended on which
// address the caller happened to use — so `git clone` could fail on an org repo
// while a hand-rolled request to the same repo succeeded.
//
// It is one credential per box now, and this is the one that wins. Store a
// token here and it becomes the account's GitHub credential everywhere: `git
// clone`, `git push`, the REST API, and every endpoint the host composes. The
// sign-in token stays in custody — it is the fallback, and it is what a box
// gets again if you remove this one — but it stops reaching boxes while this
// exists.
//
// Which is worth saying plainly: this is not additional access, it is
// DIFFERENT access. Prefer a fine-grained token limited to what your boxes
// need, and `yas integrations rm github` is the rollback.
//
// # What the attachment does, and does not do
//
// `-attach` still decides where the integration HOSTNAME applies. It does not
// narrow the credential: a box's stored row records no profile, so honouring an
// attachment when choosing the credential would mean a box came back from a
// restart holding a different GitHub identity than it was built with. The
// account-level rule is what makes the create and the resume agree.
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
	fmt.Fprintf(os.Stderr, "  A box reaches GitHub at http://%s.int.yetanothersandbox.dev/ — the whole REST API,\n", githubIntegrationID)
	fmt.Fprintln(os.Stderr, "  including /search/issues, which the built-in /ghapi route cannot do.")
	fmt.Fprintln(os.Stderr, "\n  This token now REPLACES the one `yas login` stores, on every box and at every")
	fmt.Fprintln(os.Stderr, "  door: git clone, git push and the REST API all spend this one. Boxes made")
	fmt.Fprintln(os.Stderr, "  before now keep what they were built with; make a new box to pick it up.")
	fmt.Fprintln(os.Stderr, "  `yas integrations rm github` puts the sign-in token back.")
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
