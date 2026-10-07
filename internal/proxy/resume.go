package proxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/shellstate"
)

// Resume makes the shell about to start come back as the session left it:
// set before Run, which writes the script that brings it back.
func (p *Proxy) Resume(st session.Saved) {
	if st.Shell.Cwd == "" {
		st.Shell.Cwd = p.sess.LastCwd()
	}
	p.resumed = &st
}

// resume switches the shell to session id: the journal at once, the shell
// at its next prompt, when __aish_precmd sources the script written here.
// The script is the other session's functions, aliases and variables, so
// only the user switches, as clear says.
func (p *Proxy) resume(ctx context.Context, id string) (_ rpc.Info, err error) {
	if err := session.CheckID(id); err != nil {
		return rpc.Info{}, err
	}
	defer func() {
		if err == nil {
			p.stopBackground() // with the session they belong to
		}
	}()
	fg := p.fromShell(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.asking {
		return rpc.Info{}, errors.New("sessions are switched by the user, not by the assistant")
	}
	if fg != nil {
		return rpc.Info{}, fg
	}
	if id == p.sess.ID {
		return rpc.Info{}, fmt.Errorf("already in session %s", id)
	}
	dir := p.sess.Dir()
	next, err := session.Load(dir, id)
	if err != nil {
		return rpc.Info{}, err
	}
	saved, err := session.LoadState(dir, id)
	if err != nil {
		return rpc.Info{}, err
	}
	if err := next.Lock(); err != nil {
		return rpc.Info{}, err
	}
	if saved.Shell.Cwd == "" {
		saved.Shell.Cwd = next.LastCwd()
	}
	// The change from this shell as it is now to how it started plus what
	// the other session changed: this session's own changes are undone.
	// A session of another shell brings back its directory alone.
	change := saved.Shell
	if p.base != nil && p.cur != nil {
		change = shellstate.Diff(*p.cur, shellstate.Apply(*p.base, saved.Shell.Of(p.base.Kind)))
	}
	// CheckID already keeps quotes out; the quoting is a second line. The
	// word is quoted whole for set -k, as in clear.
	script := p.shell.RestoreScript(change) + "export 'AISH_SESSION=" + next.ID + "'\n"
	if err := os.WriteFile(filepath.Join(p.run, "restore.bash"), []byte(script), 0o600); err != nil {
		next.Unlock()
		return rpc.Info{}, err
	}
	p.sess.Unlock()
	p.sess = next
	p.folds = nil
	p.switched = true
	p.restoreModel(saved)
	return p.info(), nil
}
