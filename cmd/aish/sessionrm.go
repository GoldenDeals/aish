package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
)

// sessionRmCmd removes saved sessions, found as `aish resume` finds one.
// All are found before any is removed: a typo in the last must not leave
// the job half done.
func sessionRmCmd(cfg config.Config, args []string) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return fail(errors.New("usage: aish session rm ID|NAME..."))
	}
	dir, info, err := sessionsDir(cfg)
	if err != nil {
		return fail(err)
	}
	cur := info.SessionID
	list, err := session.List(dir)
	if err != nil {
		return fail(err)
	}
	var picks []session.Info
	seen := map[string]bool{}
	for _, q := range args {
		i, err := session.Find(list, q)
		if err != nil {
			return fail(err)
		}
		if i.ID == cur {
			return fail(fmt.Errorf("%s is the session of this shell; use aish clear", i.Title()))
		}
		if !seen[i.ID] {
			seen[i.ID] = true
			picks = append(picks, i)
		}
	}
	code := 0
	for _, i := range picks {
		if err := session.Remove(dir, i.ID); err != nil {
			code = fail(err)
			continue
		}
		fmt.Println("removed " + described(i))
	}
	return code
}

// sessionPruneCmd removes the sessions not used for a while. Not the one
// of this shell: the proxy holds it open, and Prune keeps open ones.
func sessionPruneCmd(cfg config.Config, args []string) int {
	if len(args) == 0 && os.Getenv("AISH_SOCK") != "" {
		// sessions_ttl as aish applied it, not as config.toml on disk may
		// have it now.
		cwd, _ := os.Getwd()
		a, err := inForce(cfg, cwd, nil, false)
		if err != nil {
			return fail(err)
		}
		fmt.Fprint(os.Stderr, changedNote(a.changed))
		cfg = a.cfg
	}
	ttl, err := parsePrune(cfg, args)
	if err != nil {
		return fail(err)
	}
	dir, _, err := sessionsDir(cfg)
	if err != nil {
		return fail(err)
	}
	list, err := session.List(dir)
	if err != nil {
		return fail(err)
	}
	removed, err := session.Prune(dir, ttl)
	byID := map[string]session.Info{}
	for _, i := range list {
		byID[i.ID] = i
	}
	for _, id := range removed {
		i, ok := byID[id]
		if !ok {
			i.ID = id
		}
		fmt.Println("removed " + described(i))
	}
	switch len(removed) {
	case 0:
		fmt.Println("\x1b[2mno sessions to remove\x1b[0m")
	case 1:
		fmt.Println("\x1b[2m1 session removed\x1b[0m")
	default:
		fmt.Printf("\x1b[2m%d sessions removed\x1b[0m\n", len(removed))
	}
	if err != nil {
		return fail(err)
	}
	return 0
}

// parsePrune reads `aish session prune [--older AGE]`: without the flag
// the age is sessions_ttl, which is off by default.
func parsePrune(cfg config.Config, args []string) (time.Duration, error) {
	const pruneUsage = "usage: aish session prune [--older AGE] (AGE: 30d, 12h; sessions_ttl by default)"
	var age string
	switch {
	case len(args) == 0:
		if ttl := cfg.SessionsMaxAge(); ttl > 0 {
			return ttl, nil
		}
		return 0, errors.New(pruneUsage)
	case len(args) == 2 && args[0] == "--older":
		age = args[1]
	case len(args) == 1 && strings.HasPrefix(args[0], "--older="):
		age = strings.TrimPrefix(args[0], "--older=")
	default:
		return 0, errors.New(pruneUsage)
	}
	ttl, err := config.ParseAge(age)
	if err != nil {
		return 0, fmt.Errorf("--older %s: %w", age, err)
	}
	return ttl, nil
}

// sessionsDir is where the sessions are, and info the proxy's about the
// shell: inside aish the proxy's directory, the sessions_dir it started
// with, which only a restart changes, whatever config.toml on disk says
// now; outside, or with a proxy too old to tell, cfg's.
func sessionsDir(cfg config.Config) (string, rpc.Info, error) {
	var info rpc.Info
	client, err := rpc.FromEnv()
	if err != nil {
		return cfg.SessionsDir, info, nil
	}
	if err := client.Call(rpc.MethodInfo, nil, &info); err != nil {
		return "", info, err
	}
	if info.Dir == "" {
		return cfg.SessionsDir, info, nil
	}
	return info.Dir, info, nil
}

// described is the session's id, with its name if it has one.
func described(i session.Info) string {
	if i.Name != "" {
		return fmt.Sprintf("%s (%s)", i.ID, i.Name)
	}
	return i.ID
}
