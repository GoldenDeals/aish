package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// The session row is the spend of the host's turns and of its subagents'
// together, the subagents row their part; neither changes the size of
// the context, which the turns of subagents are no part of.
func TestContextSubagentSpend(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Chdir(home)
	host := []session.Entry{
		{Kind: session.KindUser, Text: "look around", Cwd: home},
		{Kind: session.KindAssistant, Text: "done", InputTokens: 7000, CachedTokens: 5000, OutputTokens: 300},
	}
	es := append(host[:1:1],
		session.Entry{Kind: session.KindUsage, About: "explore", Usage: &session.Usage{Input: 5000, Cached: 1500, Output: 100}},
		host[1],
	)
	st := rpc.Status{
		Info:        rpc.Info{SessionID: "s1", Window: 200_000},
		ToolCalls:   1,
		InputTokens: 7000, CachedTokens: 5000, OutputTokens: 300,
		SubInputTokens: 5000, SubCachedTokens: 1500, SubOutputTokens: 100,
	}
	l, err := net.Listen("unix", filepath.Join(t.TempDir(), "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go rpc.Serve(l, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		switch method {
		case rpc.MethodStatus:
			return st, nil
		case rpc.MethodHistory:
			return es, nil
		case rpc.MethodConfig:
			return rpc.Config{Config: config.Default()}, nil
		}
		return nil, errors.New("unexpected " + method)
	})
	t.Setenv("AISH_SOCK", l.Addr().String())

	code, _, stderr := captured(t, func() int { return contextCmd(config.Default(), nil) })
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(stderr, "")
	used := session.Tokens(host, config.Default().MaxOutputBytes, 0)
	for _, s := range []string{
		"used             " + session.Short(used.Tokens) + " / 200k (3%)\n",
		"session          1 tool calls, 0 compacts, 12k in (6.5k cached) / 400 out tokens spent\n",
		"subagents        of those, 5.0k in (1.5k cached) / 100 out\n",
	} {
		if !strings.Contains(plain, s) {
			t.Errorf("no %q in\n%s", s, plain)
		}
	}
	if used.Tokens != 7300 || !used.Measured {
		t.Errorf("context %+v, not the host's last turn", used)
	}

	// Without subagents, or from a proxy that does not count them, no row
	// of theirs and the host's spend alone.
	own := st
	own.SubInputTokens, own.SubCachedTokens, own.SubOutputTokens = 0, 0, 0
	if all, subs := spend(own); subs != "" || !strings.Contains(all, " 7.0k in (5.0k cached) / 300 out ") {
		t.Errorf("no subagents: %q, %q", all, subs)
	}
}
