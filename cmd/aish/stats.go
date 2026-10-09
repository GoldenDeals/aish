package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/session"
)

// statsCmd shows what the agent spent over the last 24 hours, 7, 30 and 90
// days, in all sessions: tokens, requests and sessions, in total and by
// model. The sessions are those of the proxy's directory inside aish,
// which only a restart changes, of sessions_dir outside. A journal that
// cannot be read is left out and named.
func statsCmd(cfg config.Config, args []string) int {
	if len(args) > 0 {
		return fail(errors.New("usage: aish stats"))
	}
	dir, _, err := sessionsDir(cfg)
	if err != nil {
		return fail(err)
	}
	st, err := session.CollectStats(dir, session.Periods, time.Now())
	printStats(os.Stdout, st)
	if err != nil {
		return fail(err)
	}
	return 0
}

// statsCols are the columns of aish stats, a Spend each.
var statsCols = []struct {
	name string
	of   func(session.Spend) int
}{
	{"requests", func(s session.Spend) int { return s.Requests }},
	{"sessions", func(s session.Spend) int { return s.Sessions }},
	{"input", func(s session.Spend) int { return s.Input }},
	{"cached", func(s session.Spend) int { return s.Cached }},
	{"output", func(s session.Spend) int { return s.Output }},
}

// printStats prints a table of st: a row a period, the total first, then
// the same for each model under its name.
func printStats(w io.Writer, st session.Stats) {
	label := len("all models") - 2
	for _, p := range st.Periods {
		label = max(label, len(p.Name))
	}
	fmt.Fprintf(w, "\x1b[1m%-*s\x1b[0m\x1b[2m", label+2, "all models")
	for _, c := range statsCols {
		fmt.Fprintf(w, " %9s", c.name)
	}
	fmt.Fprint(w, "\x1b[0m\n")
	rows := func(sp []session.Spend) {
		for i, p := range st.Periods {
			fmt.Fprintf(w, "  \x1b[2m%-*s\x1b[0m", label, p.Name)
			for _, c := range statsCols {
				fmt.Fprintf(w, " %9s", session.Short(c.of(sp[i])))
			}
			fmt.Fprintln(w)
		}
	}
	rows(st.Total)
	for _, m := range st.Models {
		fmt.Fprintf(w, "\n\x1b[1m%s\x1b[0m\n", m.Model)
		rows(m.Spend)
	}
}
