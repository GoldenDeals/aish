package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// The first request of a session has the model name it, after the reply
// and once: `aish resume --all` lists the session by that name.
func TestTitleAfterFirstRequest(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{
		{Text: "two files"},
		{Text: "Listing the files"},
		{Text: "still two"},
	}}
	p, _, cwd := hosted(t, prov)
	p.titles.on = true
	dir := p.sess.Dir()

	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "list files", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + cwd})
	p.waitTitles(5 * time.Second)
	list, err := session.List(dir)
	if err != nil || len(list) != 1 || list[0].AutoName != "Listing the files" || list[0].Name != "" {
		t.Fatalf("%v %+v", err, list)
	}

	// Not again: the session has a request.
	p.titles.on = true
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "and now?", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + cwd})
	p.waitTitles(5 * time.Second)
	if prov.calls != 3 {
		t.Errorf("%d calls of the model, want 3", prov.calls)
	}
}

// A call for the name that fails costs the request nothing, and tells
// nothing.
func TestTitleFails(t *testing.T) {
	prov := &scripted{replies: []*llm.Response{{Text: "two files"}}}
	p, out, cwd := hosted(t, prov)
	p.titles.on = true
	p.marker(Marker{Kind: "ask-start"})
	if _, err := call(t, p, rpc.MethodAgentStart, rpc.AgentParams{Text: "list files", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + cwd})
	p.waitTitles(5 * time.Second)
	if prov.calls != 2 {
		t.Errorf("%d calls of the model, want 2", prov.calls)
	}
	if found, _ := filepath.Glob(filepath.Join(p.sess.Dir(), "*.title")); len(found) > 0 {
		t.Errorf("titled %v", found)
	}
	if s := out.String(); !strings.Contains(s, "two files") || strings.Contains(s, "no reply scripted") {
		t.Errorf("terminal %q", s)
	}
}

// `aish session rename` names this shell's session through the proxy: in
// memory till the session is on disk, there from then on.
func TestRenameSession(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	dir := sess.Dir()
	name := func() string {
		b, _ := os.ReadFile(filepath.Join(dir, p.sess.ID+".name"))
		return strings.TrimSpace(string(b))
	}
	rename := func(n string) (rpc.Info, error) {
		v, err := call(t, p, rpc.MethodRename, rpc.RenameParams{Name: n})
		if err != nil {
			return rpc.Info{}, err
		}
		return v.(rpc.Info), nil
	}

	if info, err := rename(" work "); err != nil || info.Name != "work" || name() != "" {
		t.Fatalf("%+v %v, on disk %q", info, err, name())
	}
	sess.Append(session.Entry{Kind: session.KindShell, Cmd: "ls"})
	if name() != "work" {
		t.Errorf("on disk %q", name())
	}
	if info, err := rename(""); err != nil || info.Name != "" || name() != "" {
		t.Errorf("%+v %v, on disk %q", info, err, name())
	}

	other, _ := session.New(dir)
	other.Append(session.Entry{Kind: session.KindShell, Cmd: "pwd"})
	if err := other.SetName("taken"); err != nil {
		t.Fatal(err)
	}
	if _, err := rename("taken"); err == nil || p.sess.Name() != "" {
		t.Errorf("a name taken: %v", err)
	}
}
