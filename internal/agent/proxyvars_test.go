package agent

import (
	"strings"
	"testing"
)

// git, curl, wget and pip read their proxy from variables in lower case:
// under Bash(git *) https_proxy=… git push sends the push through a proxy
// of the subagent's choice. Of the lower case names only these are kept
// from it; the script's own variables are set as before.
func TestScopedBashProxyVars(t *testing.T) {
	dir := t.TempDir()
	git := &bashScope{patterns: []string{"git *", "env *"}}
	for _, cmd := range []string{
		`x=1; git log -n "$x"`,
		"LC_ALL=C git log",
		"env x=1 git log",
		"proxy=x; git log",
		"https_proxy_url=x; git log",
	} {
		if why := refused(git, cmd, dir, nil); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
	for cmd, want := range map[string]string{
		"https_proxy=http://x git push":               "sets https_proxy",
		"export all_proxy=x; git fetch":               "sets all_proxy",
		"env no_proxy=x git fetch":                    "sets no_proxy",
		"http_proxy=x git fetch":                      "sets http_proxy",
		"ftp_proxy=x; git fetch":                      "sets ftp_proxy",
		"declare socks_proxy=x; git ls-remote origin": "sets socks_proxy",
		"git fetch ${rsync_proxy:=x}":                 "sets rsync_proxy",
		"env -uno_proxy git fetch":                    "sets no_proxy",
	} {
		why := refused(git, cmd, dir, nil)
		if why == "" || !strings.Contains(why, want) {
			t.Errorf("%q: %q, want a refusal with %q in it", cmd, why, want)
		}
	}
}
