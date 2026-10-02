// Package shellinit holds the bash integration script that aish installs
// after the user's ~/.bashrc.
package shellinit

import _ "embed"

//go:embed init.bash
var Bash string

// RCFile is passed to `bash --rcfile`: the user's own configuration first,
// then the aish hooks.
func RCFile() string {
	return "[ -f ~/.bashrc ] && . ~/.bashrc\n" + Bash
}
