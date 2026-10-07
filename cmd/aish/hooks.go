package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/hooks"
)

// hooksCmd lists the hooks a request in cwd would run and what is wrong
// with them: a pre-tool hook may turn a call down or change its
// arguments, and this is where to see which could have. It runs none.
func hooksCmd(cfg config.Config, args []string) int {
	if len(args) > 0 {
		return fail(errors.New("usage: aish hooks"))
	}
	cwd, _ := os.Getwd()
	a, err := inForce(cfg, cwd, nil, false)
	if err != nil {
		return fail(err)
	}
	fmt.Fprint(os.Stderr, changedNote(a.changed))
	printHooks(os.Stdout, a.cfg, a.project)
	return 0
}

// printHooks lists the hooks of cfg, of a directory with project its
// project file laid over, as the agent finds them, the project file's with
// them. Its hooks_dir counts only when the file is trusted; one that is
// not is named, lest its hooks be taken for running.
func printHooks(w io.Writer, cfg config.Config, project string) {
	set, problems := hooks.Find(cfg.HooksDir)
	listHooks(w, set, problems, cfg.HooksDir)
	if slices.Contains(cfg.Untrusted, "hooks_dir") {
		fmt.Fprintf(w, "%s sets hooks_dir, but is not trusted: its hooks run after aish trust\n", home(project))
	}
}

// listHooks prints the hooks of set by event, in the order they run, and
// the problems Find met. dirs is where Find looked, a list in the form of
// PATH.
func listHooks(w io.Writer, set *hooks.Set, problems []error, dirs string) {
	nameW, n := 0, 0
	for _, e := range hooks.Events {
		for _, h := range set.For(e) {
			nameW = max(nameW, runewidth.StringWidth(h.Name))
			n++
		}
	}
	if n == 0 {
		fmt.Fprintf(w, "no hooks in %s\n", hookDirs(dirs))
	}
	for _, e := range hooks.Events {
		hs := set.For(e)
		if len(hs) == 0 {
			continue
		}
		fmt.Fprintf(w, "\x1b[1m%s\x1b[0m\n", e)
		for _, h := range hs {
			fmt.Fprintf(w, "  %s  %s\n", runewidth.FillRight(h.Name, nameW), home(h.Path))
		}
	}
	for _, p := range problems {
		// Find's problems begin with the path.
		fmt.Fprintf(w, "\x1b[33mproblem:\x1b[0m %s\n", home(p.Error()))
	}
}

// hooksLine is the row of aish status: how many hooks a request here
// runs, of which events, from where, and whether aish hooks has problems
// to show.
func hooksLine(dirs string) string {
	set, problems := hooks.Find(dirs)
	n := 0
	var by []string
	for _, e := range hooks.Events {
		if k := len(set.For(e)); k > 0 {
			n += k
			by = append(by, fmt.Sprintf("%s %d", e, k))
		}
	}
	line := "none in " + hookDirs(dirs)
	if n > 0 {
		line = fmt.Sprintf("%d (%s) in %s", n, strings.Join(by, ", "), hookDirs(dirs))
	}
	switch len(problems) {
	case 0:
	case 1:
		line += "; 1 problem, aish hooks"
	default:
		line += fmt.Sprintf("; %d problems, aish hooks", len(problems))
	}
	return line
}

// hookDirs names the directories of a hooks_dir list, ~ for the home.
func hookDirs(list string) string {
	var ds []string
	for _, d := range filepath.SplitList(list) {
		if d != "" {
			ds = append(ds, home(d))
		}
	}
	if len(ds) == 0 {
		return `hooks_dir ""`
	}
	return strings.Join(ds, ", ")
}
