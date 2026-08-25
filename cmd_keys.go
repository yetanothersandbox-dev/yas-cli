package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
)

// cmdKeys is the service-account surface: mint a named key for a machine,
// list what exists, revoke what is done. Everything acts on the CALLER's own
// tenant — there is no operator in this loop, which is the point.
func cmdKeys(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch args[0] {
	case "list", "ls":
		keys, err := cl.Keys(ctx)
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			fmt.Fprintln(os.Stderr, "no keys — which is odd, because one of them made this request")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tCREATED\tSTATE")
		for _, k := range keys {
			state := "live"
			if !k.Live {
				state = "revoked"
			}
			if k.Current {
				state += " (this one)"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", k.ID, k.Name, k.CreatedAt.Format("2006-01-02"), state)
		}
		return w.Flush()

	case "create", "new":
		if len(args) != 2 {
			return errors.New("usage: yas keys create <name>  (name the machine that will hold it)")
		}
		id, secret, err := cl.CreateKey(ctx, args[1])
		if err != nil {
			return err
		}
		// The secret to STDOUT alone, once — pipeable straight into wherever
		// the machine keeps it. Everything human goes to stderr.
		fmt.Fprintf(os.Stderr, "key %s (%s) minted; the secret follows on stdout, ONCE:\n", id, args[1])
		fmt.Println(secret)
		return nil

	case "revoke", "rm":
		if len(args) != 2 {
			return errors.New("usage: yas keys revoke <key-id>  (ids from `yas keys list`)")
		}
		if err := cl.RevokeKey(ctx, args[1]); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "revoked; it stops authenticating immediately")
		return nil

	default:
		return fmt.Errorf("unknown keys command %q: list, create <name>, revoke <id>", args[0])
	}
}
