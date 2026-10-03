package proxy

import (
	"errors"
	"fmt"
)

// tellDefErr says why config.toml selects no profile for the shell: err is
// what config.LoadEnv failed with where the shell's own profile did not,
// on a profile $AISH_PROFILE or the profile key names and config.toml has
// not. The request goes on with the shell's profile, but the status and a
// shell whose profile is gone go by what config.toml selected before, so
// the user is told; once while the same error lasts, not with every
// request until config.toml is mended. The error starts with the path of
// config.toml, which the line leaves out. Called under reqMu.
func (p *Proxy) tellDefErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err == nil {
		p.defErr = ""
		return
	}
	if inner := errors.Unwrap(err); inner != nil {
		err = inner
	}
	if err.Error() == p.defErr {
		return
	}
	p.defErr = err.Error()
	p.emit(fmt.Appendf(nil, "\x1b[2m[aish: config.toml: %v]\x1b[0m\r\n", err))
}
