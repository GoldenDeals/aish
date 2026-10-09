#compdef aish
# Tab completion of aish for zsh: put it in a directory of $fpath as _aish
# before compinit runs, or source it after. Autoloaded, this file is the
# body of _aish, which defines _aish anew and calls it. A shell under aish
# has it already: init.zsh's __aish_comp_aish is this function.
_aish() {
	local -a v
	v=("${(@f)$("${AISH_BIN:-aish}" __complete "${(@)words[1,CURRENT-1]}" "$PREFIX" </dev/null 2>/dev/null)}")
	compadd -- "${(@)v:#}"
}
if [[ ${funcstack[1]-} == _aish ]]; then
	_aish "$@"
elif (( ${+functions[compdef]} )); then
	compdef _aish aish
fi
