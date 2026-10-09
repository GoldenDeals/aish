package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/tools"
)

// A request from a repository and the next one, a day later, from a
// directory outside any: the system prompt is the same, so that the
// provider reads the history before them from its cache; the git root and
// the date are in the header of each request.
func TestSystemPromptStable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	prov := &fakeProvider{replies: []*llm.Response{{Text: "one"}, {Text: "two"}}}
	a, j, _, _, plain := newAgent(t, prov)
	home := filepath.Dir(plain)
	// The ceiling keeps git from finding a repository the temporary
	// directory may be in.
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CEILING_DIRECTORIES=" + home, "USER=u"}
	proj := filepath.Join(home, "proj")
	sub := filepath.Join(proj, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "init", "-q", proj)
	git.Env = env
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	root, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatal(err)
	}

	if err := a.Start(context.Background(), "in the repo", tools.Exec{Dir: sub, Env: env}); err != nil {
		t.Fatal(err)
	}
	if j.es[0].Kind != session.KindUser || j.es[0].Repo != root {
		t.Fatalf("request %+v, want the git root %s", j.es[0], root)
	}
	yesterday := time.Now().Add(-24 * time.Hour)
	for i := range j.es {
		j.es[i].Time = yesterday
	}
	if err := a.Start(context.Background(), "outside", tools.Exec{Dir: plain, Env: env}); err != nil {
		t.Fatal(err)
	}
	if len(prov.requests) != 2 {
		t.Fatalf("%d requests", len(prov.requests))
	}
	first, second := prov.requests[0], prov.requests[1]
	if first.System != second.System {
		t.Errorf("the system prompt changed:\n%s\n---\n%s", first.System, second.System)
	}
	for _, no := range []string{"Git repository", "Today's date", time.Now().Format("2006-01-02"), plain, proj} {
		if strings.Contains(second.System, no) {
			t.Errorf("the system prompt has %q:\n%s", no, second.System)
		}
	}
	if got := first.Messages[len(first.Messages)-1].Text; !strings.HasSuffix(got, ", cwd "+sub+", git root "+root+"]\nin the repo") {
		t.Errorf("the first request:\n%s", got)
	}
	head := "[" + yesterday.Format("2006-01-02 15:04") + ", cwd " + sub + ", git root " + root + "]\nin the repo"
	if got := second.Messages[0].Text; got != head {
		t.Errorf("the first request a day later:\n%s\nwant\n%s", got, head)
	}
	last := second.Messages[len(second.Messages)-1].Text
	if !strings.HasSuffix(last, ", cwd "+plain+"]\noutside") || strings.Contains(last, "git root") {
		t.Errorf("the second request:\n%s", last)
	}
	if j.es[len(j.es)-2].Repo != "" {
		t.Errorf("a git root outside a repository: %+v", j.es[len(j.es)-2])
	}
}

// The environment names the model that answers, and the user is the
// shell's, not the proxy's.
func TestEnvironmentModelAndUser(t *testing.T) {
	a, _, _, _, cwd := newAgent(t, &fakeProvider{})
	t.Setenv("USER", "proxyuser")
	a.exec = tools.Exec{Dir: cwd, Env: []string{"USER=shelluser"}}
	a.Cfg.Model, a.Cfg.Provider = "claude-opus-5", ""
	sys := a.request(nil).System
	if !strings.Contains(sys, "\n- Model: claude-opus-5 (anthropic)") {
		t.Errorf("no model:\n%s", sys)
	}
	if !strings.Contains(sys, ", user shelluser\n") || strings.Contains(sys, "proxyuser") {
		t.Errorf("not the shell's user:\n%s", sys)
	}
	if env := environment("", "gpt-5", "openai", "u"); !strings.HasSuffix(env, "\n- Model: gpt-5 (openai)") {
		t.Errorf("another provider:\n%s", env)
	}
}

// A request recorded before Entry.Repo, or made outside a repository, has
// the header it always had.
func TestMessagesRequestHeader(t *testing.T) {
	at := time.Date(2026, 10, 9, 14, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		e    session.Entry
		want string
	}{
		{session.Entry{Kind: session.KindUser, Time: at, Cwd: "/home/u/p", Text: "q"}, "[2026-10-09 14:00, cwd /home/u/p]\nq"},
		{session.Entry{Kind: session.KindUser, Time: at, Cwd: "/home/u/p/sub", Repo: "/home/u/p", Text: "q"},
			"[2026-10-09 14:00, cwd /home/u/p/sub, git root /home/u/p]\nq"},
	} {
		ms := Messages([]session.Entry{tc.e}, 1000, nil)
		if len(ms) != 1 || ms[0].Text != tc.want {
			t.Errorf("%+v: %+v, want %q", tc.e, ms, tc.want)
		}
	}
}
