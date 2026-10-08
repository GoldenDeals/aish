package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/proxy"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// resumeCmd brings a session back: inside aish this shell switches to it,
// outside a new aish starts with it, with conf, cfg's config files.
func resumeCmd(conf *config.Snapshot, cfg config.Config, args []string) int {
	if len(args) > 1 || len(args) == 1 && strings.HasPrefix(args[0], "-") {
		return fail(errors.New("usage: aish resume [ID|NAME]"))
	}
	var client *rpc.Client
	cur, curSaved := "", false
	dir := cfg.SessionsDir
	if c, err := rpc.FromEnv(); err == nil {
		client = c
		var info rpc.Info
		if err := client.Call(rpc.MethodInfo, nil, &info); err != nil {
			return fail(err)
		}
		// The proxy refuses the switch anyway, but only after the picker
		// was shown or the session looked up.
		if info.Asking {
			return fail(errors.New("sessions are switched by the user, not by the assistant"))
		}
		cur, curSaved = info.SessionID, info.Saved
		// The proxy switches to a session of its own directory, whatever
		// sessions_dir config.toml on disk has now.
		if info.Dir != "" {
			dir = info.Dir
		}
	}
	list, err := session.List(dir)
	if err != nil {
		return fail(err)
	}
	var pick session.Info
	if len(args) == 1 {
		if pick, err = session.Find(list, args[0]); err != nil {
			return fail(err)
		}
	} else {
		if len(list) == 0 {
			return fail(errors.New("no sessions yet"))
		}
		if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
			return fail(errors.New("usage: aish resume ID|NAME (choosing needs a terminal)"))
		}
		var ok bool
		if pick, ok, err = pickSession(dir, list, cur, cfg.Profile); err != nil {
			return fail(err)
		} else if !ok {
			return 0
		}
	}
	switch {
	case pick.ID == cur:
		return fail(fmt.Errorf("this shell is in session %s already", pick.Title()))
	case pick.Open:
		return fail(fmt.Errorf("session %s is open in another aish", pick.Title()))
	}

	if client != nil {
		var info rpc.Info
		if err := client.Call(rpc.MethodResume, rpc.ResumeParams{ID: pick.ID}, &info); err != nil {
			return fail(err)
		}
		if !curSaved {
			fmt.Fprintln(os.Stderr, "\x1b[2mthe session you left was not saved\x1b[0m")
		}
		if sess, err := session.Load(dir, pick.ID); err == nil {
			printResumed(pick, sess, cfg.Profile)
		}
		return 0
	}
	sess, err := session.Load(dir, pick.ID)
	if err != nil {
		return fail(err)
	}
	return startShell(conf, cfg, sess, true)
}

// startShell runs the shell under aish with sess, brought back as it was left
// if resume. conf is the config files cfg is of: the proxy goes by them.
func startShell(conf *config.Snapshot, cfg config.Config, sess *session.Session, resume bool) int {
	p := proxy.New(sess)
	if resume {
		saved, err := session.LoadState(cfg.SessionsDir, sess.ID)
		if err != nil {
			return fail(err)
		}
		p.Resume(saved)
		if list, err := session.List(cfg.SessionsDir); err == nil {
			for _, i := range list {
				if i.ID == sess.ID {
					printResumed(i, sess, cfg.Profile)
				}
			}
		}
	}
	code, err := p.Run(conf, cfg)
	if err != nil {
		return fail(err)
	}
	return code
}

// printResumed reminds what the session was about: its last entries.
func printResumed(i session.Info, sess *session.Session, def string) {
	if n := sess.BadLines(); n > 0 {
		fmt.Fprintf(os.Stderr, "aish: %s.jsonl: %d bad lines\n", sess.ID, n)
	}
	es := session.Current(sess.Entries())
	var parts []string
	if i.Name != "" {
		parts = append(parts, i.ID)
	}
	parts = append(parts, fmt.Sprintf("%d requests", i.Requests), "last used "+ago(i.Modified))
	if i.Cwd != "" {
		parts = append(parts, home(i.Cwd))
	}
	if m := sessionModel(i, def); m != "" {
		parts = append(parts, m)
	}
	fmt.Printf("\x1b[1mresumed %s\x1b[0m \x1b[2m(%s)\x1b[0m\n", i.Title(), strings.Join(parts, " · "))
	const tail = 8
	if len(es) > tail {
		fmt.Printf("\x1b[2m… %d earlier entries (aish session show)\x1b[0m\n", len(es)-tail)
		es = es[len(es)-tail:]
	}
	for _, e := range es {
		printEntry(e)
	}
}

// sessionModel is where a session's requests go: the profile and the
// model, "local · qwen3:8b", config.Root for the top level; the model alone
// when the profile is def, the one config.toml selects, which most
// sessions have and the status by the prompt does not name either, and for
// a state saved before there were profiles, whose profile is the shell's;
// "" when the session has no model saved.
func sessionModel(i session.Info, def string) string {
	switch {
	case i.Model == "":
		return ""
	case (i.Profile != "" || i.TopLevel) && i.Profile != def:
		return profileName(i.Profile) + " · " + i.Model
	}
	return i.Model
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Format("2 Jan 2006")
}

func home(path string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "/" {
		if path == h {
			return "~"
		}
		if rest, ok := strings.CutPrefix(path, h+"/"); ok {
			return "~/" + rest
		}
	}
	return path
}
