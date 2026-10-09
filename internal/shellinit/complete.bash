# Tab completion of aish for bash: source it from ~/.bashrc, or let
# bash-completion load it as the file aish in its completions directory
# (~/.local/share/bash-completion/completions/aish). A shell under aish has
# it already: init.bash's __aish_comp_aish is this function.
_aish() {
	local c q
	COMPREPLY=()
	while IFS= read -r c; do
		printf -v q %q "$c"
		[[ $q == "${2-}"* ]] && COMPREPLY+=("$q")
	done < <("${AISH_BIN:-aish}" __complete "${COMP_WORDS[@]:0:COMP_CWORD}" "${2-}" </dev/null 2>/dev/null)
	return 0
}
complete -F _aish aish
