package proxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// clear starts this shell's session over for `aish clear` and `aish new`:
// the session left stays on disk, as every one with entries is, and the
// next is named cp.Name if given. A name taken changes nothing. Only the
// user, at the shell's foreground, clears: the agent's command is refused
// by p.asking, and between requests a process in the background — a
// subagent's command, a job the agent's command left — by fromShell.
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
	left, err := p.newSession(cp.Name)
	if err != nil {
		return rpc.Info{}, err
	}
	left.Unlock()
	return p.info(), nil
}

// newSession moves the shell to a new session, named name if given, and
// returns the one it leaves: on disk if it had an entry, and locked still,
// for the caller to let go of or to keep. The shell exports the new id at
// its next prompt. A name taken changes nothing. Called under p.mu.
func (p *Proxy) newSession(name string) (*session.Session, error) {
	next := p.sess.Next()
	if err := next.SetName(name); err != nil {
		return nil, err
	}
	if p.run != "" {
		// Appended: a resume may have left a script the shell has not run yet.
		f, err := os.OpenFile(filepath.Join(p.run, "restore.bash"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		// The word quoted whole: under set -k an unquoted NAME=VALUE goes
		// to the environment of export, which then lists the exports.
		_, err = fmt.Fprintf(f, "export 'AISH_SESSION=%s'\n", next.ID)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
	}
	left := p.sess
	p.sess = next
	return left, nil
}
