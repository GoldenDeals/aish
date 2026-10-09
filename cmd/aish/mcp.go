package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/mcp"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// mcpCmd shows how the MCP servers are doing. Inside aish it asks the proxy,
// which runs them, those of the MCP config in force: the file it names is
// the one aish read at its start or at the last `aish apply-config`, not
// the one config.toml on disk may name now. Outside it only reads the
// config and the cache, starting nothing.
func mcpCmd(cfg config.Config, args []string) int {
	if len(args) > 0 {
		return fail(errors.New("usage: aish mcp"))
	}
	var res mcp.StatusResult
	live := false
	if client, err := rpc.FromEnv(); err == nil {
		cwd, _ := os.Getwd()
		a, err := inForce(cfg, cwd, nil, false)
		if err != nil {
			return fail(err)
		}
		cfg = a.cfg
		fmt.Fprint(os.Stderr, changedNote(a.changed))
		if err := client.Call(rpc.MethodMCPStatus, nil, &res); err != nil {
			return fail(err)
		}
		live = true
	} else {
		servers, err := config.NewSnapshot().Servers()
		if err != nil {
			return fail(err)
		}
		res = mcp.NewManager(mcp.FromConfig(servers), filepath.Join(config.CacheDir(), "mcp")).Status()
	}

	if len(res.Servers) == 0 {
		fmt.Printf("no MCP servers in %s\n", home(cfg.MCPConfig))
		return 0
	}
	for _, s := range res.Servers {
		state := s.State
		switch s.State {
		case "running":
			state = "\x1b[32m● running\x1b[0m"
		case "starting":
			state = "\x1b[33m◌ starting\x1b[0m"
		case "failed":
			state = "\x1b[31m✕ failed\x1b[0m"
		case "exited":
			state = "\x1b[33m○ exited\x1b[0m"
		case "idle":
			state = "\x1b[2m○ idle\x1b[0m"
		case "new":
			state = "\x1b[2m○ not started yet\x1b[0m"
		}
		fmt.Printf("\x1b[1m%s\x1b[0m  %s\n", s.Name, state)
		fmt.Printf("  \x1b[2m%-9s\x1b[0m %s %s\n", "transport", s.Transport, s.Target)
		if s.Expose != "" {
			fmt.Printf("  \x1b[2m%-9s\x1b[0m %s\n", "expose", s.Expose)
		}
		if len(s.Tools) > 0 {
			fmt.Printf("  \x1b[2m%-9s\x1b[0m %d: %s\n", "tools", len(s.Tools), strings.Join(s.Tools, ", "))
		}
		if s.Error != "" {
			when := ""
			if !s.Failed.IsZero() {
				when = " \x1b[2m(" + ago(s.Failed) + ")\x1b[0m"
			}
			first, rest, _ := strings.Cut(strings.TrimSpace(s.Error), "\n")
			fmt.Printf("  \x1b[2m%-9s\x1b[0m \x1b[31m%s\x1b[0m%s\n", "error", first, when)
			// A server that died quotes its stderr: its end tells why.
			if lines := strings.Split(rest, "\n"); rest != "" {
				if len(lines) > 3 {
					fmt.Printf("            \x1b[2m… %d lines\x1b[0m\n", len(lines)-3)
					lines = lines[len(lines)-3:]
				}
				for _, l := range lines {
					fmt.Printf("            \x1b[2m%s\x1b[0m\n", strings.TrimRight(l, " \t\r"))
				}
			}
		}
	}
	for _, n := range res.Notes {
		fmt.Printf("\x1b[33mnote:\x1b[0m %s\n", n)
	}
	fmt.Printf("\x1b[2mconfig %s", home(cfg.MCPConfig))
	if !live {
		fmt.Print(" · outside aish: from the cache, nothing started")
	}
	fmt.Print("\x1b[0m\n")
	return 0
}
