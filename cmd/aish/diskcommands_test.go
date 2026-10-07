package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/mcp"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// oldSession is a saved session in dir, not used for two days.
func oldSession(t *testing.T, dir string) string {
	t.Helper()
	s, err := session.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.Unlock()
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, s.ID+".jsonl"), old, old); err != nil {
		t.Fatal(err)
	}
	return s.ID
}

// exists tells whether session id of dir is still on disk.
func exists(dir, id string) bool {
	_, err := os.Stat(filepath.Join(dir, id+".jsonl"))
	return err == nil
}

// Inside aish, with config.toml broken on disk, aish mcp, skills, agents,
// session and resume go by the proxy: the MCP config, the tools and
// sessions_ttl in force, the directory of the sessions the proxy has.
// Outside aish the broken file stops them, as before.
func TestCommandsBrokenOnDisk(t *testing.T) {
	disk := diskConfig(t, "max_steps = -1\n")
	sessions := t.TempDir() // the proxy's, not sessions_dir of the file
	applied := config.Default()
	applied.MCPConfig = filepath.Join(t.TempDir(), "servers.yaml")
	applied.ToolsDir = t.TempDir()
	applied.SessionsTTL = "1d"
	// A tool of the config in force takes the name of a skill here.
	tool := "#!/bin/sh\n# aish:desc A tool\necho hi\n"
	if err := os.WriteFile(filepath.Join(applied.ToolsDir, "zzdeploy190"), []byte(tool), 0o755); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(".claude", "skills", "zzdeploy190", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("---\nname: zzdeploy190\ndescription: Deploy\n---\nDeploy.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeProxy(t, map[string]any{
		rpc.MethodConfig:    rpc.Config{Config: applied},
		rpc.MethodInfo:      rpc.Info{SessionID: "cur", Dir: sessions, Saved: true},
		rpc.MethodMCPStatus: mcp.StatusResult{},
		rpc.MethodResume:    rpc.Info{},
	})

	code, stdout, stderr := captured(t, func() int { return run([]string{"mcp"}) })
	if code != 0 || stderr != "" || !strings.Contains(stdout, "no MCP servers in "+applied.MCPConfig) {
		t.Errorf("aish mcp: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr = captured(t, func() int { return run([]string{"skills"}) })
	if code != 0 || stderr != "" || !strings.Contains(stdout, "zzdeploy190") || !strings.Contains(stdout, "a tool has this name") {
		t.Errorf("aish skills: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, _, stderr := captured(t, func() int { return run([]string{"agents"}) }); code != 0 || stderr != "" {
		t.Errorf("aish agents: exit %d, stderr %q", code, stderr)
	}

	// The sessions are the proxy's: those of sessions_dir on disk stay.
	kept := oldSession(t, disk.SessionsDir)
	byAge, byID, resumed := oldSession(t, sessions), oldSession(t, sessions), oldSession(t, sessions)
	if code, _, stderr := captured(t, func() int { return run([]string{"resume", resumed}) }); code != 0 {
		t.Errorf("aish resume: exit %d, stderr %q", code, stderr)
	}
	if code, _, stderr := captured(t, func() int { return run([]string{"session", "rm", byID}) }); code != 0 || exists(sessions, byID) {
		t.Errorf("aish session rm: exit %d, stderr %q", code, stderr)
	}
	if code, _, stderr := captured(t, func() int { return run([]string{"session", "prune", "--older", "1h"}) }); code != 0 || exists(sessions, byAge) {
		t.Errorf("aish session prune --older: exit %d, stderr %q", code, stderr)
	}
	// sessions_ttl of the file, broken or not, is "0": no age to go by.
	byTTL := oldSession(t, sessions)
	if code, _, stderr := captured(t, func() int { return run([]string{"session", "prune"}) }); code != 0 || exists(sessions, byTTL) {
		t.Errorf("aish session prune by sessions_ttl in force: exit %d, stderr %q", code, stderr)
	}
	if !exists(disk.SessionsDir, kept) {
		t.Error("a session of sessions_dir on disk was removed")
	}

	t.Setenv("AISH_SOCK", "")
	for _, args := range [][]string{{"mcp"}, {"skills"}, {"agents"}, {"session", "prune", "--older", "1h"}, {"resume", kept}} {
		if code, _, stderr := captured(t, func() int { return run(args) }); code == 0 || !strings.Contains(stderr, "max_steps") {
			t.Errorf("aish %q outside aish: exit %d, stderr %q", args, code, stderr)
		}
	}
	if !exists(disk.SessionsDir, kept) {
		t.Error("outside aish a broken config.toml removed a session")
	}
}
