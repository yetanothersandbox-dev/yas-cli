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
	case "transcript", "log":
		return scheduleTranscript(ctx, args[1:])
	case "now", "run":
		return scheduleNow(ctx, args[1:])
	default:
		return fmt.Errorf("unknown schedule command %q: list, add, show, rm, pause, resume, runs, transcript, now", args[0])
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
		// -model is in the hint because this same file refuses a schedule
		// without one eleven lines later. A suggestion the product then
		// rejects teaches the refusal instead of the command.
		fmt.Fprintln(os.Stderr, `  yas schedule add nightly -cron "0 3 * * *" -tz Europe/London -model claude-sonnet-5 "triage new issues"`)
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tWHEN\tNEXT\tMODEL\tLAST\tPROMPT")
	for _, s := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.ID, cronColumn(s), nextColumn(s), modelColumn(s), lastColumn(s), truncateOneLine(s.Prompt(), 40))
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

// modelColumn names what this schedule spends on. A schedule with none cannot
// run at all, so the gap is worth showing rather than leaving blank.
func modelColumn(s api.Schedule) string {
	if m := s.Model(); m != "" {
		return m
	}
	return "NONE — cannot run"
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
	fmt.Printf("%s %s\n", label("model   "), modelColumn(s))
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
		cron     = fs.String("cron", "", "when to run: a 5-field expression, e.g. \"0 3 * * *\" (03:00 daily)")
		tz       = fs.String("tz", "", "IANA timezone the expression is read in (default UTC)")
		profile  = fs.String("profile", "", "the posture each firing is created from; `yas` profiles set size, egress and repo")
		desc     = fs.String("description", "", "what this is for")
		prompt   = fs.String("prompt", "", "the prompt each firing runs; `-` reads it from stdin")
		model    = fs.String("model", "", "the model the agent runs on, e.g. claude-sonnet-5. Required: a task with none cannot run")
		provider = fs.String("provider", "", "which LLM the model belongs to: anthropic (default) or openai. Required for a gpt-* model, whose firings otherwise boot a box with no route to OpenAI")
		overlap  = fs.String("overlap", "", "what to do when the last firing is still running: skip (default) or allow")
		pause    = fs.Bool("paused", false, "write it without letting the clock have it yet")
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

	// Normalised in place, before anything reads it: the flag's spellings and
	// the wire's are not the same set ("anthropic" is written as empty), and an
	// edit merges this value into a stored task further down.
	providerName, err := parseProvider(*provider)
	if err != nil {
		return err
	}
	*provider = providerName

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
		task, text, *model, *provider = scheduleTaskPayload(existing.Task, text, *model, providerName)
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
	// Refused here as well as at the server, so the message arrives before a
	// round trip and can name the flag. A task with no model cannot run, and
	// the failure it produces inside the box names neither the cause nor the
	// fix — see the server's own refusal for why it is not defaulted.
	if *model == "" && len(task) == 0 {
		return errors.New("a schedule needs -model: a task with none cannot run, and the box fails with " +
			"\"the harness produced no agent turn: Connection error\", which explains nothing.\n" +
			"  It is not defaulted because a schedule runs unattended and forever, so the model it spends on " +
			"is a decision to make once.\n" +
			"  For example: -model claude-sonnet-5, -model claude-opus-5, -model claude-haiku-4-5")
	}

	req := api.ScheduleRequest{
		Description: *desc,
		Cron:        *cron,
		Timezone:    *tz,
		Profile:     *profile,
		Prompt:      text,
		Model:       *model,
		Provider:    *provider,
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

// scheduleTaskPayload decides how a write names the work: `task` or
// `prompt`+`model`, never both — the server refuses a body carrying the two.
//
// An EDIT always goes back as the raw task, merged. A stored task can carry
// fields this client cannot name (a turn budget, a system prompt), and PUT
// replaces the whole row, so anything rebuilt from the fields it happens to
// understand is a field silently cleared. Only the values this invocation
// actually gave are overwritten.
//
// A CREATE has no stored task, so the shorthand is all there is.
func scheduleTaskPayload(existing json.RawMessage, prompt, model, provider string) (json.RawMessage, string, string, string) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(existing, &fields) != nil || len(fields) == 0 {
		return nil, prompt, model, provider
	}
	set := func(key, value string) {
		if value == "" {
			return
		}
		if enc, err := json.Marshal(value); err == nil {
			fields[key] = enc
		}
	}
	set("prompt", prompt)
	set("model", model)
	// Only when this invocation named one. An edit that says nothing about the
	// provider must leave the stored one alone: it is fixed for the life of
	// every box the schedule fires, and clearing it would silently move a
	// running OpenAI schedule onto the Anthropic table.
	set("provider", provider)
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, prompt, model, provider
	}
	return out, "", "", ""
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
	fmt.Fprintln(w, "RUN\tWHEN\tOUTCOME\tKEPT\tBOX\tDETAIL")
	kept := false
	for _, f := range rows {
		box := f.SandboxID
		if box == "" {
			box = "-"
		}
		if f.Transcript != nil {
			kept = true
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			f.ID, f.DueAt.Local().Format("Jan 2 15:04"), f.Outcome,
			keptColumn(f), box, truncateOneLine(f.Detail, 60))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	// The two ways to read a run, and they have different lifetimes. The kept
	// copy is the one that still answers next week; the box answers for about
	// two hours after it finishes and holds things the copy does not — the
	// egress log, the policy, a shell.
	if kept {
		fmt.Fprintf(os.Stderr, "\nread one: `yas schedule transcript %s <run>`\n", name)
	}
	for _, f := range rows {
		if f.SandboxID != "" {
			fmt.Fprintf(os.Stderr, "the box itself, while it is still up: `yas ssh %s`\n", f.SandboxID)
			break
		}
	}
	return nil
}

// keptColumn says whether a run can still be read, in one word.
//
// "-" is not "no transcript exists"; it is "none was kept", which for a run
// that started seconds ago simply means the copier has not been round yet. The
// two are told apart by the age of the run, which is the column beside it.
//
// "live" is a copy of a run that is STILL GOING — everything it has written so
// far, kept up to date as it goes. It is readable now and it will grow.
func keptColumn(f api.Firing) string {
	if f.Transcript == nil {
		return "-"
	}
	switch {
	case !f.Transcript.Final:
		return "live"
	case f.Transcript.Events == 0:
		return "empty"
	case f.Transcript.Truncated:
		return "part"
	}
	return "yes"
}

// scheduleTranscript prints one kept run.
//
// One JSON frame per line, and deliberately: the frames are the host's own, the
// dashboard renders them as a conversation, and a second renderer here would be
// a second set of rules to keep in step with that one. NDJSON is what a
// terminal can actually work with — `| jq` beats any formatting this could do.
func scheduleTranscript(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: yas schedule transcript <name> <run>\n" +
			"  `yas schedule runs <name>` lists the runs and which of them were kept")
	}
	name, runID := args[0], args[1]
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	t, err := cl.RunTranscript(ctx, name, runID)
	if err != nil {
		return err
	}
	if t.Transcript != nil && t.Transcript.Note != "" {
		// On stderr, so it cannot land in the middle of a pipe. It is about the
		// COPY and not about the run — "the box was already gone" is not
		// something the agent did.
		fmt.Fprintf(os.Stderr, "note: %s\n", t.Transcript.Note)
	}
	if len(t.Events) == 0 {
		fmt.Fprintf(os.Stderr, "%s has no frames. The box was retired before the copy was taken.\n", runID)
		return nil
	}
	enc := json.NewEncoder(os.Stdout)
	for _, e := range t.Events {
		if err := enc.Encode(e); err != nil {
			return err
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
