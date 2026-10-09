package shellinit

import _ "embed"

// CompleteBash and CompleteZsh are what `aish completion bash|zsh`
// prints: Tab for the command aish in a shell not under aish, which the
// installer puts where bash-completion or compinit finds it. Under aish
// init.bash and init.zsh give the shell the same (__aish_comp_aish), and
// Tab for @path and /skill besides. Either asks aish for the words:
// `aish __complete WORD...`, the line up to the word under the cursor,
// that word last, prints what may stand there, a word a line.
//
//go:embed complete.bash
var CompleteBash string

//go:embed complete.zsh
var CompleteZsh string
