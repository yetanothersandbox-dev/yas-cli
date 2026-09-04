package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Gilbert09/yas/clients/yas/internal/api"
	"github.com/Gilbert09/yas/clients/yas/internal/ui"
)

// cmdSchedule manages recurring work: a prompt, a posture and a cadence.
//
// Each firing is a FRESH box that runs the prompt and then stops itself, so a
// schedule holds none of your pool between firings and leaves nothing to clean
// up. The clock is the server's, not this laptop's — a schedule fires whether
// or not anything of yours is running.
func cmdSchedule(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	ctx := context.Background()
	switch args[0] {
	case "list", "ls":
		return scheduleList(ctx)
	case "add", "put", "edit", "set":
		return scheduleAdd(ctx, args[1:])
	case "show", "get":
		return scheduleShow(ctx, args[1:])
	case "rm", "remove", "delete":
		return scheduleRemove(ctx, args[1:])
	case "pause":
		return schedulePause(ctx, args[1:], false)
	case "resume", "unpause":
		return schedulePause(ctx, args[1:], true)
	case "runs", "history":
		return scheduleRuns(ctx, args[1:])
	case "now", "run":
		return scheduleNow(ctx, args[1:])
	default:
		return fmt.Errorf("unknown schedule command %q: list, add, show, rm, pause, resume, runs, now", args[0])
	}
}

func scheduleList(ctx context.Context) error {
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	rows, err := cl.Schedules(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "no schedules. Add one:")
		fmt.Fprintln(os.Stderr, `  yas schedule add nightly -cron "0 3 * * *" -tz Europe/London "triage new issues"`)
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tWHEN\tNEXT\tLAST\tPROMPT")
	for _, s := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			s.ID, cronColumn(s), nextColumn(s), lastColumn(s), truncateOneLine(s.Prompt(), 48))
	}
	return w.Flush()
}

// cronColumn is the expression plus its zone, because "0 3 * * *" on its own is
// three o'clock somewhere and the somewhere is the whole point of the column.
func cronColumn(s api.Schedule) string {
	tz := s.Timezone
	if tz == "" {
		tz = "UTC"
	}
	return s.Cron + " " + tz
}

// nextColumn answers the question a listing exists for: is this thing going to
// run, and when.
func nextColumn(s api.Schedule) string {
	if !s.Enabled {
		return "paused"
	}
	if s.NextRunAt == nil {
		// Enabled, and nothing will fire it. Rare — an expression whose next
		// match is years out — and worth saying rather than showing a blank.
		return "never"
	}
	if s.NextRunIn > 0 {
		// The SERVER's countdown. Subtracting from the local clock would report
		// the wrong answer on a laptop whose clock has drifted, silently.
		return "in " + shortDuration(time.Duration(s.NextRunIn)*time.Second)
	}
	return "due"
}

func lastColumn(s api.Schedule) string {
	if s.LastRun == nil {
		return "-"
	}
	return s.LastRun.Outcome + " " + s.LastRun.FiredAt.Local().Format("Jan 2 15:04")
}

func scheduleShow(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: yas schedule show <name>")
	}
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	s, err := cl.Schedule(ctx, args[0])
	if err != nil {
		return err
	}
	tty := ui.StderrTTY()
	label := func(k string) string {
		if !tty {
			return k
		}
		return ui.S(ui.Subtle).Render(k)
	}
	fmt.Printf("%s %s\n", label("schedule"), s.ID)
	if s.Description != "" {
		fmt.Printf("%s %s\n", label("about   "), s.Description)
	}
	fmt.Printf("%s %s\n", label("when    "), cronColumn(s))
	fmt.Printf("%s %s\n", label("next    "), nextColumn(s))
	if s.NextRunAt != nil && s.Enabled {
		fmt.Printf("%s %s\n", label("        "), s.NextRunAt.Local().Format("Mon 2 Jan 2006 15:04 MST"))
	}
	if s.Profile != "" {
		fmt.Printf("%s %s\n", label("profile "), s.Profile)
	}
	fmt.Printf("%s %s\n", label("overlap "), s.Overlap)
	fmt.Printf("%s %s\n", label("prompt  "), s.Prompt())
	if s.LastRun != nil {
		fmt.Printf("%s %s at %s", label("last    "), s.LastRun.Outcome,
			s.LastRun.FiredAt.Local().Format("Mon 2 Jan 15:04"))
		if s.LastRun.SandboxID != "" {
			fmt.Printf(" (%s)", s.LastRun.SandboxID)
		}
		fmt.Println()
	}
	return nil
}

func scheduleAdd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("schedule add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		cron    = fs.String("cron", "", "when to run: a 5-field expression, e.g. \"0 3 * * *\" (03:00 daily)")
		tz      = fs.String("tz", "", "IANA timezone the expression is read in (default UTC)")
		profile = fs.String("profile", "", "the posture each firing is created from; `yas` profiles set size, egress and repo")
		desc    = fs.String("description", "", "what this is for")
		prompt  = fs.String("prompt", "", "the prompt each firing runs; `-` reads it from stdin")
		overlap = fs.String("overlap", "", "what to do when the last firing is still running: skip (default) or allow")
		pause   = fs.Bool("paused", false, "write it without letting the clock have it yet")
	)
	// The name is taken off the front before parsing, for the reason
	// integrationsAdd spells out: Go's flag package stops at the first bare
	// word, so `add nightly -cron ...` would otherwise read every flag as a
	// positional argument and silently ignore it.
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return errors.New(scheduleAddUsage + flagsOf(fs))
	}
	rest := fs.Args()
	if name == "" {
		if len(rest) == 0 {
			return errors.New(scheduleAddUsage + flagsOf(fs))
		}
		name, rest = rest[0], rest[1:]
	}

	// The prompt is the trailing words when -prompt was not given, so the
	// commonest form reads as a sentence:
	//   yas schedule add nightly -cron "0 3 * * *" "triage new issues"
	text := *prompt
	if text == "" && len(rest) > 0 {
		text = strings.Join(rest, " ")
		rest = nil
	}
	if len(rest) > 0 {
		return fmt.Errorf("unexpected argument %q; give the prompt once, either as -prompt or as the trailing words", rest[0])
	}
	if text == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		text = strings.TrimSpace(string(b))
	}

	_, cl, err := loadClient()
	if err != nil {
		return err
	}

	// An EDIT may leave the prompt and the cron alone; a CREATE may not. So the
	// existing row is read first and used to fill what was not given — without
	// it, `yas schedule edit nightly -cron "0 4 * * *"` would blank the prompt,
	// which is the one field somebody would not notice was gone until 04:00.
	existing, err := cl.Schedule(ctx, name)
	isNew := err != nil && api.IsNotFound(err)
	if err != nil && !isNew {
		return err
	}
	task := json.RawMessage(nil)
	if !isNew {
		// The work goes back as the RAW task, not as a prompt dug out of it and
		// re-wrapped: a task written through the API can carry fields this
		// client has never heard of — a model, a turn budget — and PUT replaces
		// the whole row, so a re-wrapped prompt would silently clear the rest.
		task, text = scheduleTaskPayload(existing.Task, text)
		if *cron == "" {
			*cron = existing.Cron
		}
		if *tz == "" {
			*tz = existing.Timezone
		}
		if *profile == "" {
			*profile = existing.Profile
		}
		if *overlap == "" {
			*overlap = existing.Overlap
		}
		if *desc == "" {
			*desc = existing.Description
		}
	}
	if *cron == "" {
		return errors.New(`a schedule needs -cron, for example -cron "0 3 * * *" (03:00 every day)`)
	}
	if text == "" && len(task) == 0 {
		return errors.New("a schedule needs a prompt: without one it boots a box every time it fires and the box does nothing")
	}

	req := api.ScheduleRequest{
		Description: *desc,
		Cron:        *cron,
		Timezone:    *tz,
		Profile:     *profile,
		Prompt:      text,
		Task:        task,
		Overlap:     *overlap,
	}
	// Sent only when this invocation actually said something about it: the
	// server reads an absent `enabled` as "leave it as it is", so an edit that
	// never mentioned pausing must not carry a value.
	if *pause {
		no := false
		req.Enabled = &no
	} else if isNew {
		yes := true
		req.Enabled = &yes
	}

	s, err := cl.PutSchedule(ctx, name, req)
	if err != nil {
		return err
	}
	verb := "updated"
	if isNew {
		verb = "created"
	}
	fmt.Fprintf(os.Stderr, "schedule %s %s — %s\n", s.ID, verb, cronColumn(s))
	if !s.Enabled {
		fmt.Fprintf(os.Stderr, "it is PAUSED. `yas schedule resume %s` starts the clock; "+
			"`yas schedule now %s` runs it once without.\n", s.ID, s.ID)
		return nil
	}
	if s.NextRunAt != nil {
		fmt.Fprintf(os.Stderr, "next firing %s (%s)\n",
			s.NextRunAt.Local().Format("Mon 2 Jan 15:04 MST"), nextColumn(s))
	}
	fmt.Fprintf(os.Stderr, "check it now with `yas schedule now %s`, and read what happened with `yas schedule runs %s`\n", s.ID, s.ID)
	return nil
}

const scheduleAddUsage = "usage: yas schedule add <name> -cron \"<expression>\" [flags] <prompt...>\n"

// scheduleTaskPayload decides how an edit writes the work back: `task` or
// `prompt`, never both — the server refuses a body carrying the two.
//
// No new prompt round-trips the stored task verbatim. A new prompt replaces
// only the task's own prompt when the task holds anything else, and takes the
// shorthand otherwise. Either way, a field this client cannot name survives
// the edit.
func scheduleTaskPayload(existing json.RawMessage, text string) (json.RawMessage, string) {
	if text == "" {
		return existing, ""
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(existing, &fields) != nil || len(fields) == 0 {
		return nil, text
	}
	extra := false
	for k := range fields {
		if k != "prompt" {
			extra = true
			break
		}
	}
	if !extra {
		return nil, text
	}
	enc, err := json.Marshal(text)
	if err != nil {
		return nil, text
	}
	fields["prompt"] = enc
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, text
	}
	return out, ""
}

func scheduleRemove(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: yas schedule rm <name>")
	}
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	if err := cl.DeleteSchedule(ctx, args[0]); err != nil {
		return err
	}
	// Said out loud, because the other reading is the dangerous one: this stops
	// the schedule making new boxes and does not touch a box it already made.
	fmt.Fprintf(os.Stderr, "schedule %s deleted. Boxes it already started keep running — `yas list` shows them.\n", args[0])
	return nil
}

func schedulePause(ctx context.Context, args []string, enable bool) error {
	verb := "pause"
	if enable {
		verb = "resume"
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: yas schedule %s <name>", verb)
	}
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	// Read-then-write, because the server's PUT is a whole-row replace: sending
	// only `enabled` would blank the cron and the prompt.
	s, err := cl.Schedule(ctx, args[0])
	if err != nil {
		return err
	}
	req := api.ScheduleRequest{
		Description: s.Description, Cron: s.Cron, Timezone: s.Timezone,
		Profile: s.Profile, Task: s.Task, Overlap: s.Overlap, Enabled: &enable,
	}
	out, err := cl.PutSchedule(ctx, args[0], req)
	if err != nil {
		return err
	}
	if !out.Enabled {
		fmt.Fprintf(os.Stderr, "schedule %s paused; nothing will fire it until you resume it\n", out.ID)
		return nil
	}
	fmt.Fprintf(os.Stderr, "schedule %s resumed — next firing %s\n", out.ID, nextColumn(out))
	return nil
}

func scheduleRuns(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("schedule runs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	limit := fs.Int("n", 20, "how many firings to show, newest first")
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return errors.New("usage: yas schedule runs <name> [-n <count>]\n" + flagsOf(fs))
	}
	if name == "" {
		if rest := fs.Args(); len(rest) == 1 {
			name = rest[0]
		} else {
			return errors.New("usage: yas schedule runs <name> [-n <count>]")
		}
	}

	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	rows, err := cl.Firings(ctx, name, *limit)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintf(os.Stderr, "%s has not fired yet. `yas schedule now %s` runs it once.\n", name, name)
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "WHEN\tOUTCOME\tBOX\tDETAIL")
	for _, f := range rows {
		box := f.SandboxID
		if box == "" {
			box = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			f.DueAt.Local().Format("Jan 2 15:04"), f.Outcome, box, truncateOneLine(f.Detail, 60))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	// The point of holding the box id: a firing is a handle to a transcript.
	// A box that has finished and been reaped no longer answers, which is why
	// this is a hint rather than a promise.
	for _, f := range rows {
		if f.SandboxID != "" {
			fmt.Fprintf(os.Stderr, "\nread one: `yas exec %s -- cat /var/log/agent.log`, or `yas ssh %s` while it is still up\n",
				f.SandboxID, f.SandboxID)
			break
		}
	}
	return nil
}

func scheduleNow(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: yas schedule now <name>")
	}
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	f, err := cl.RunSchedule(ctx, args[0])
	if err != nil {
		return err
	}
	// Said every time, because "run it now" reads like "run tonight's early"
	// and it is not: the next scheduled firing is untouched.
	fmt.Fprintf(os.Stderr, "fired %s -> %s\n", args[0], f.SandboxID)
	fmt.Fprintf(os.Stderr, "this is an extra run beside the schedule; the next scheduled firing is unchanged\n")
	return nil
}

// shortDuration renders a countdown the way somebody reads it aloud: "4h12m",
// "3d", "45s". Never a Go duration with sub-second parts in it.
func shortDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// truncateOneLine flattens a prompt onto one line for a table.
//
// Prompts are multi-line often enough that this matters: a newline in a
// tabwriter column breaks the alignment of every row below it, so the table
// stops being a table at the first paragraph somebody wrote.
func truncateOneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
