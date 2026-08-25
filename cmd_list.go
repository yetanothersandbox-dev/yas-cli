package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/Gilbert09/yas/clients/yas/internal/api"
)

// boxRow is one listed sandbox with its per-id detail filled in.
type boxRow struct {
	api.SandboxSummary
	Status  string
	MemMiB  int
	MemUsed int
	Err     error
}

// fetchRows is the list + bounded Get fan-out. The gateway's index cannot
// answer status — it changes every second and lives on the host — so `list`
// asks per id, eight at a time.
func fetchRows(ctx context.Context, cl *api.Client) ([]boxRow, error) {
	sums, err := cl.List(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]boxRow, len(sums))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, s := range sums {
		rows[i].SandboxSummary = s
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sb, err := cl.Get(ctx, id)
			if err != nil {
				rows[i].Err = err
				rows[i].Status = "?"
				return
			}
			rows[i].Status = sb.Status
			rows[i].MemMiB = sb.MemMiB
			rows[i].MemUsed = sb.MemUsedMiB
		}(i, s.ID)
	}
	wg.Wait()
	sort.Slice(rows, func(a, b int) bool { return rows[a].CreatedAt.After(rows[b].CreatedAt) })
	return rows, nil
}

// cmdList prints the fleet-shaped truth about this tenant's boxes — which
// deliberately does not include what machine any of them is on.
func cmdList(args []string) error {
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	rows, err := fetchRows(context.Background(), cl)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "no boxes — `yas new` makes one")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tAGE\tMEM")
	for _, r := range rows {
		mem := ""
		if r.MemMiB > 0 {
			mem = fmt.Sprintf("%d/%dMiB", r.MemUsed, r.MemMiB)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.ID, r.Status, age(r.CreatedAt), mem)
	}
	return w.Flush()
}

// age renders a compact duration ("3h", "2d") — enough to pick a box by.
func age(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
