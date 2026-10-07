package proxy

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
)

// The options the shell had on at its last prompt, as __aish_dump printed
// them with set +o and shopt -p, go with the agent's request for the
// policy; before the first dump none are known.
func TestShellExecOptions(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.run = t.TempDir()
	ap := rpc.AgentParams{Cwd: "/srv", Env: []string{"HOME=/home/me"}}
	if ex := p.shellExec(ap); ex.Opts != nil || ex.Dir != "/srv" || !slices.Equal(ex.Env, ap.Env) {
		t.Errorf("before a dump: %+v", ex)
	}
	opts := "set +o errexit\nset -o keyword\nset -o braceexpand\nshopt -s cdable_vars\nshopt -u extglob\n"
	for _, f := range []string{"state.base", "state"} {
		if err := os.WriteFile(filepath.Join(p.run, f), []byte("\x00\x00\x00"+opts), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p.mu.Lock()
	p.saveState("/srv")
	p.mu.Unlock()
	ex := p.shellExec(ap)
	slices.Sort(ex.Opts)
	if want := []string{"braceexpand", "cdable_vars", "keyword"}; !slices.Equal(ex.Opts, want) || ex.Dir != "/srv" {
		t.Errorf("options %q, want %q (%+v)", ex.Opts, want, ex)
	}
}
