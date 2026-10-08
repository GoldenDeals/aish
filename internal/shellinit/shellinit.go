// Package shellinit holds the integration scripts aish installs in the
// user's shell after its own configuration: init.bash after ~/.bashrc,
// init.zsh after ~/.zshrc. The proxy speaks to both alike: the markers,
// $AISH_RUN and `aish agent start|resume` (the head of init.bash).
package shellinit

import _ "embed"

//go:embed init.bash
var Bash string

//go:embed init.zsh
var Zsh string

// RCFile is passed to `bash --rcfile`: the user's own configuration first,
// then the aish hooks.
func RCFile() string {
	return "[ -f ~/.bashrc ] && . ~/.bashrc\n" + Bash
}

// ZshEnv and ZshRC are .zshenv and .zshrc of the ZDOTDIR aish starts zsh
// with, $AISH_RUN/zsh: zsh comes there for .zshrc, which sources the
// user's and then init.zsh. The user's files are read from where they would
// have been: ZDOTDIR as it was, in AISH_ZDOTDIR when it was set, is back
// before the user's .zshenv, and so is what that file left before the
// user's .zshrc.
const ZshEnv = `# aish: ZDOTDIR is aish's only for zsh to come here for .zshrc.
typeset -g __aish_zdot=$ZDOTDIR
if (( ${+AISH_ZDOTDIR} )); then
	export ZDOTDIR=$AISH_ZDOTDIR
	unset AISH_ZDOTDIR
else
	unset ZDOTDIR
fi
[[ -f ${ZDOTDIR:-$HOME}/.zshenv ]] && builtin source "${ZDOTDIR:-$HOME}/.zshenv"
(( ${+ZDOTDIR} )) && typeset -g __aish_zdotdir=$ZDOTDIR
ZDOTDIR=$__aish_zdot
unset __aish_zdot
`

// ZshRC is the .zshrc of ZshEnv.
func ZshRC() string {
	return `# aish: the user's .zshrc, then aish's integration.
if (( ${+__aish_zdotdir} )); then ZDOTDIR=$__aish_zdotdir; else unset ZDOTDIR; fi
unset __aish_zdotdir
[[ -f ${ZDOTDIR:-$HOME}/.zshrc ]] && builtin source "${ZDOTDIR:-$HOME}/.zshrc"
` + Zsh
}
