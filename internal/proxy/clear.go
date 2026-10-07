package proxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/GoldenDeals/aish/internal/bashstate"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// clear starts this shell's session over for `aish clear` and `aish new`,
// saving the current one first or the next one from the start if asked.
// The names are checked before anything is saved or dropped: a taken one
// changes nothing. Only the user, at the shell's foreground, clears: the
// agent's command is refused by p.asking, and between requests a process
// in the background — a subagent's command, a job the agent's command
// left — by fromShell.
func (p *Proxy) clear(ctx context.Context, cp rpc.ClearParams) (_ rpc.Info, err error) {
	defer func() {
		if err == nil {
			p.stopBackground() // with the session they belong to
		}
	}()
	fg := p.fromShell(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.asking {
		return rpc.Info{}, errors.New("sessions are cleared by the user, not by the assistant")
	}
	if fg != nil {
		return rpc.Info{}, fg
	}
	// A name without its journal on disk would be a file of nothing.
	if cp.Name != "" && !cp.Save || cp.NewName != "" && !cp.SaveNew {
		return rpc.Info{}, errors.New("only a session that is saved takes a name")
	}
	dir := p.sess.Dir()
	if cp.Name != "" {
		if err := session.CheckName(dir, p.sess.ID, cp.Name); err != nil {
			return rpc.Info{}, err
		}
	}
	if cp.NewName != "" {
		if err := session.CheckName(dir, "", cp.NewName); err != nil {
			return rpc.Info{}, err
		}
	}
	if cp.Save && !p.sess.Saved() {
		if p.sess.Len() == 0 {
			return rpc.Info{}, errors.New("session is empty, nothing to save")
		}
		if err := p.sess.Save(); err != nil {
			return rpc.Info{}, err
		}
		// The shell as the last prompt found it: right before the user
		// typed `aish clear save`.
		st := p.modelState()
		if p.base != nil && p.cur != nil {
			st.Shell = bashstate.Diff(*p.base, *p.cur)
		}
		if err := session.SaveState(dir, p.sess.ID, st); err != nil {
			return rpc.Info{}, err
		}
	}
	if cp.Name != "" {
		if err := session.Rename(dir, p.sess.ID, cp.Name); err != nil {
			return rpc.Info{}, err
		}
	}
	p.sess.Clear()
	if cp.SaveNew {
		if err := p.sess.Save(); err != nil {
			return rpc.Info{}, err
		}
		if cp.NewName != "" {
			if err := session.Rename(dir, p.sess.ID, cp.NewName); err != nil {
				return rpc.Info{}, err
			}
		}
	}
	if p.run != "" {
		// Appended: a resume may have left a script the shell has not run yet.
		f, err := os.OpenFile(filepath.Join(p.run, "restore.bash"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return rpc.Info{}, err
		}
		// The word quoted whole: under set -k an unquoted NAME=VALUE goes
		// to the environment of export, which then lists the exports.
		_, err = fmt.Fprintf(f, "export 'AISH_SESSION=%s'\n", p.sess.ID)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return rpc.Info{}, err
		}
	}
	return p.info(), nil
}
