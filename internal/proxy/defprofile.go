package proxy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/inebotov/aish/internal/config"
)

// tellDefErr says why config.toml selects no profile for the shell: err is
// what config.LoadEnv failed with where the shell's own profile did not,
// on a profile $AISH_PROFILE or the profile key names and config.toml has
// not. The request goes on with the shell's profile, but the status goes
// by what config.toml selected before, and a shell whose profile is gone
// goes to the top level, so the user is told; once while the same error
// lasts, not with every request until config.toml is mended. The error
// starts with the path of config.toml, which the line leaves out. Called
// under reqMu.
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

// tellConfigPath says the shell has another $AISH_CONFIG than the proxy,
// exported or unset there after aish started: a command there that reads
// config.toml, `aish resume` say, reads the file it names, while the agent,
// `aish model` and `aish status` go by the one aish started with. The proxy
// keeps its own, which aish read as it started, before there was a shell
// to ask. Once for a value while it lasts. Called under reqMu.
func (p *Proxy) tellConfigPath(getenv func(string) string) {
	def := filepath.Join(config.Dir(), "config.toml")
	// The proxy's own environment, not the shell's: the one
	// config.LoadProfile reads the file by.
	reads, shell := os.Getenv("AISH_CONFIG"), getenv("AISH_CONFIG")
	if reads == "" {
		reads = def
	}
	in := shell
	if in == "" {
		in = def
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if filepath.Clean(in) == filepath.Clean(reads) {
		p.shellConfig = ""
		return
	}
	if shell == "" {
		shell = "unset"
	}
	if shell == p.shellConfig {
		return
	}
	p.shellConfig = shell
	p.emit(fmt.Appendf(nil, "\x1b[2m[aish: AISH_CONFIG in the shell is %s, aish reads %s; restart aish to switch]\x1b[0m\r\n", shell, reads))
}
