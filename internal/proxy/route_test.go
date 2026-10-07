package proxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/session"
)

func TestRouteFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	run, err := makeRunDir("/opt/aish", "n", config.Default().Route)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(run)
	b, err := os.ReadFile(filepath.Join(run, "route"))
	if want := "capital=true\nnot_found=true\nsuffix=?\nmin_words=2\nexpand=true\n"; err != nil || string(b) != want {
		t.Errorf("route: %q, %v; want %q", b, err, want)
	}
}

// A command that asks (an alias of __aish_ask, say) gives the journal the
// request, not a shell entry with the reply as its output.
func TestAskInsideCommand(t *testing.T) {
	sess, err := session.New(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.out = &terminal{}
	p.marker(Marker{Kind: "cmd-start", Payload: "q why does this fail"})
	p.marker(Marker{Kind: "ask-start"})
	p.output([]byte("Because the file is missing.\r\n"))
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
	for _, e := range sess.Entries() {
		if e.Kind == session.KindShell {
			t.Errorf("journal holds %+v", e)
		}
	}
	if p.user != nil || p.asking {
		t.Errorf("after cmd-end: user %+v, asking %v", p.user, p.asking)
	}
}
