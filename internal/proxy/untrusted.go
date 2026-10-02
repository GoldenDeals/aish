package proxy

import (
	"fmt"
	"os"
	"strings"

	"github.com/inebotov/aish/internal/config"
)

// tellUntrusted says which keys of the project file the request goes
// without, as it is not trusted: once per file and contents, not with
// every request in the repository. p.untrusted holds the files told of,
// by path and sha256. Called under reqMu.
func (p *Proxy) tellUntrusted(project string, keys []string) {
	if len(keys) == 0 {
		return
	}
	said := project + "\x00" + config.Sum(project)
	if p.untrusted[said] {
		return
	}
	if p.untrusted == nil {
		p.untrusted = map[string]bool{}
	}
	p.untrusted[said] = true
	shown := project
	if h, err := os.UserHomeDir(); err == nil && h != "/" {
		if rest, ok := strings.CutPrefix(project, h+"/"); ok {
			shown = "~/" + rest
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.emit(fmt.Appendf(nil, "\x1b[2maish: %s sets %s; they run code from the repository, so aish skips them until you run aish trust\x1b[0m\r\n",
		shown, strings.Join(keys, ", ")))
}
