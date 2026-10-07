package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"slices"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/policy"
)

// fileSum is the sha256 of the regular file at path, "" if there is none
// or it cannot be read. Not a FIFO, which would keep the read waiting.
func fileSum(path string) string {
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// tellChanged says that the config files on disk differ from those in
// force: an edit waits for `aish apply-config`, and nothing else would tell
// why it changes nothing. Once for an edit of a file, not with every
// request until it is applied. Called under reqMu.
func (p *Proxy) tellChanged(conf *config.Snapshot, pols *policy.Cache, cwd string) {
	_, keys := pols.Changed()
	keys = append(keys, conf.Changed(cwd)...)
	if f, now, changed := p.mcpChanged(); changed {
		keys = append(keys, f+"\x00"+now)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !slices.ContainsFunc(keys, func(k string) bool { return !p.confSaid[k] }) {
		return
	}
	if p.confSaid == nil {
		p.confSaid = map[string]bool{}
	}
	for _, k := range keys {
		p.confSaid[k] = true
	}
	p.emit([]byte("\x1b[2m[aish: config changed on disk: aish apply-config to apply it]\x1b[0m\r\n"))
}

// mcpChanged tells whether the MCP config the servers are of differs on
// disk now from the file read, and what it reads now.
func (p *Proxy) mcpChanged() (file, now string, changed bool) {
	p.mu.Lock()
	file, sum := p.mcpFile, p.mcpSum
	p.mu.Unlock()
	if file == "" { // none read: a proxy Run has not started
		return "", "", false
	}
	now = fileSum(file)
	return file, now, now != sum
}

// unapplied names the config files on disk that differ from those in
// force, of conf and of the policies: what `aish apply-config` would apply.
func (p *Proxy) unapplied(conf *config.Snapshot) []string {
	names := conf.Stale()
	dirs, _ := p.policies.Changed()
	names = append(names, dirs...)
	if f, _, changed := p.mcpChanged(); changed {
		names = append(names, f)
	}
	return names
}
