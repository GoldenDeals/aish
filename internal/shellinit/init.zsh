# aish zsh integration. Sourced by $AISH_RUN/zsh/.zshrc after the user's
# own .zshrc: ZDOTDIR is aish's only for zsh to come to that file.
#
# Enter runs the widget accept-line, which aish wraps: __aish_route may
# rewrite the line into `__aish_ask "$__aish_req"`, so that zsh never parses
# the natural language itself, and the accept-line from before goes on.
#
# The markers, the files of $AISH_RUN and `aish agent start|resume` are those
# of init.bash, whose head lists them: cmd-start comes from preexec, cmd-end
# from precmd, and $AISH_RUN/state is written before it as there.

[[ -o interactive ]] || \return 0
[[ -n ${__aish_loaded-} ]] && \return 0
\typeset -g __aish_loaded=1
\zmodload 'zsh/parameter' 2>/dev/null || \return 0

# The code below is read with zsh's own syntax: the user's aliases, global
# ones too, and the options that change how code is read stay off up to the
# end of the file, where they come back. The lines that turn them off are
# read with aliases on: every word is quoted.
\typeset -gA __aish_opts
__aish_opts=('aliases' "${options[aliases]}" 'ignorebraces' "${options[ignorebraces]}"
	'ignoreclosebraces' "${options[ignoreclosebraces]}" 'kshglob' "${options[kshglob]}"
	'shglob' "${options[shglob]}" 'rcquotes' "${options[rcquotes]}"
	'cshjunkiequotes' "${options[cshjunkiequotes]}")
\setopt 'noaliases' 'noignorebraces' 'noignoreclosebraces' 'nokshglob' 'noshglob' 'norcquotes' 'nocshjunkiequotes'

# A file, not the environment: every command would inherit that.
typeset -g __aish_nonce=
[[ -n ${AISH_RUN-} ]] && IFS= read -r __aish_nonce <"$AISH_RUN/nonce"

# The config's [route], key=value lines from the proxy.
typeset -g __aish_route_capital=true __aish_route_not_found=false __aish_route_suffix= __aish_route_min_words=2
typeset -g __aish_route_expand=true
if [[ -n ${AISH_RUN-} && -f $AISH_RUN/route ]]; then
	while IFS= read -r __aish_l; do
		case ${__aish_l%%=*} in
		(capital | not_found | suffix | min_words | expand) typeset -g "__aish_route_${__aish_l%%=*}=${__aish_l#*=}" ;;
		esac
	done <"$AISH_RUN/route"
	unset __aish_l
fi

[[ -n ${AISH_TOOLS_PATH-} ]] && PATH="$AISH_TOOLS_PATH:$PATH"
(( ${+commands[aish]} )) || aish() { "$AISH_BIN" "$@"; }

# The globals, declared: a function assigning one undeclared would warn
# under warn_create_global.
typeset -g __aish_buf=     # the command being entered, its lines joined
typeset -g __aish_ps0=     # the cmd-start marker preexec prints, for a user command only
typeset -g __aish_t=       # the text of a request, as __aish_route found it
typeset -g __aish_raw=     # 1: the text goes as typed (the ? prefix)
typeset -g __aish_req=     # the text __aish_ask "$__aish_req" sends
typeset -g __aish_typed=   # what was typed, for history
typeset -g __aish_redraw=  # 1: __aish_ask replaces the line on the screen
typeset -g __aish_hint= __aish_hinted= __aish_rc=0 __aish_based= __aish_autocd=off

# Every function but those that run the user's code (__aish_ask, the
# expansion of a request) and those that must leave what they do to the
# shell (__aish_precmd sourcing restore.bash) starts with emulate -L zsh:
# the user's options stay out of it.

# __aish_accept is accept-line: the line is routed first.
__aish_accept() {
	typeset -g __aish_autocd=${options[autocd]}
	__aish_route
	if [[ -n $__aish_t && -z $__aish_raw && $__aish_route_expand == true && $__aish_t == *'$'* ]]; then
		__aish_expanding
	fi
	if [[ -n $__aish_t || -n $__aish_raw ]]; then
		__aish_request
	fi
	typeset -g __aish_t= __aish_raw=
	zle __aish_accept_prev -- "$@"
}

# __aish_route decides whether BUFFER is the shell's or a request: for one it
# sets __aish_t to its text, else __aish_ps0 for preexec.
__aish_route() {
	emulate -L zsh -o extendedglob
	local __aish_line __aish_trim __aish_w __aish_end
	__aish_t= __aish_raw=
	__aish_line=$BUFFER
	if [[ -n $PREBUFFER ]]; then
		# Continuation line (PS2): part of a command already routed to zsh.
		__aish_buf+=$'\n'$__aish_line
		__aish_mark
		return
	fi
	__aish_buf=$__aish_line
	__aish_ps0= __aish_hint=
	__aish_trim=${__aish_line##[[:space:]]#}
	[[ -z $__aish_trim ]] && return

	case $__aish_trim in
	('?'*)
		__aish_t=${__aish_trim#\?} __aish_raw=1
		return
		;;
	('@'*)
		# Starts with a file mention: "@main.go what is this?"
		__aish_t=$__aish_trim
		return
		;;
	('!'*)
		# Forced shell: `!cmd` runs cmd. History expansion (`!!`, `!$`) keeps
		# its `!`, and so does negation, `!` before a blank or alone.
		case $__aish_trim in
		('!' | '!'[[:space:]]* | '!!'* | '!$'* | '!-'* | '!'[0-9]*) ;;
		(*)
			BUFFER=${__aish_trim#!}
			CURSOR=$#BUFFER
			__aish_buf=$BUFFER
			;;
		esac
		__aish_mark
		return
		;;
	esac

	# A skill typed as a command or as /name goes to the assistant as
	# `/name args`; any other command is zsh's. A line that is no command
	# is a request by [route], as in init.bash.
	__aish_w=${__aish_trim%%[[:space:]]*}
	__aish_end=${__aish_trim%%[[:space:]]#}
	if [[ $__aish_w == /* ]] && __aish_is_skill "${__aish_w#/}"; then
		__aish_t=$__aish_trim
	elif __aish_is_command "$__aish_trim"; then
		__aish_mark
	elif __aish_is_skill "$__aish_w"; then
		__aish_t="/$__aish_trim"
	elif [[ $__aish_route_capital == true && ${__aish_trim[1]} == [[:upper:]] ]] ||
		[[ -n $__aish_route_suffix && $__aish_end == *"$__aish_route_suffix" ]]; then
		__aish_t=$__aish_trim
	elif [[ $__aish_route_not_found == true ]] && __aish_is_prose "$__aish_trim"; then
		__aish_t=$__aish_trim
	else
		__aish_mark
		if [[ -z $__aish_hinted ]] && __aish_is_prose "$__aish_trim"; then
			__aish_hint=1 __aish_hinted=1 # for command_not_found_handler, once
		fi
	fi
}

# __aish_is_command decides by the first word, the way zsh itself would.
__aish_is_command() {
	emulate -L zsh
	local w
	w=${1%%[[:space:]]*}
	w=${w%%[\;\|\&\<\>\(\)]*}
	[[ -z $w ]] && return 0 # starts with an operator: let zsh complain
	case $w in
	(*=*) return 0 ;;                      # VAR=value cmd, =cmd
	(/* | ./* | ../* | '~'*) return 0 ;;   # path
	('$'* | '`'* | '"'* | "'"* | '#'* | '{' | '[[' | '((' | '!') return 0 ;;
	esac
	whence -- "$w" >/dev/null 2>&1 && return 0
	# A directory zsh goes to with autocd.
	[[ $__aish_autocd == on && -d $w ]]
}

# __aish_is_skill: is there a skill named $1 here? The roots are those of
# skills.Find (internal/skills), as in init.bash.
__aish_is_skill() {
	emulate -L zsh
	# =~ sets these: the user's stay as they were.
	local n d MATCH MBEGIN MEND match mbegin mend
	n=$1
	[[ $n =~ '^[A-Za-z0-9_-]{1,64}$' ]] || return 1
	for d in "$HOME/.claude" "${XDG_CONFIG_HOME:-$HOME/.config}/aish"; do
		[[ -f $d/skills/$n/SKILL.md ]] && return 0
	done
	d=$PWD
	while [[ -n $d ]]; do
		[[ -f $d/.claude/skills/$n/SKILL.md ]] && return 0
		d=${d%/*}
	done
	[[ -f /.claude/skills/$n/SKILL.md ]]
}

# __aish_is_prose: is $1 words rather than shell — min_words of them or
# more, and none of | & ; < > ( ) $ ` \ =? Quotes are prose: "doesn't".
__aish_is_prose() {
	emulate -L zsh
	[[ $1 == *[\|\&\;\<\>\(\)\$\`\\=]* ]] && return 1
	local n re MATCH MBEGIN MEND match mbegin mend
	n=$((__aish_route_min_words - 1))
	((n > 0)) || return 0
	re="^[^[:space:]]+([[:space:]]+[^[:space:]]+){$n}"
	[[ $1 =~ $re ]]
}

# With not_found off such a line is zsh's, and the first one gets a hint
# after "command not found". The handler from ~/.zshrc still answers.
if [[ $__aish_route_not_found != true ]]; then
	if (( ${+functions[command_not_found_handler]} )); then
		functions[__aish_cnf_prev]=${functions[command_not_found_handler]}
	fi
	command_not_found_handler() {
		emulate -L zsh
		local r
		r=127
		if (( ${+functions[__aish_cnf_prev]} )); then
			__aish_cnf_prev "$@"
			r=$?
		else
			print -ru2 -- "zsh: command not found: $1"
		fi
		[[ $__aish_hint == 1 ]] && print -u2 'aish: looks like a question; prefix with ? to ask'
		return $r
	}
fi

# __aish_expanding sets __aish_t to its text expanded: $VAR, ${...}, $(...)
# and $((...)), as in double quotes, so that the screen, the journal and the
# model get the same text, and history what was typed. It runs with the
# user's options, as the text would. A text it cannot follow stays as typed:
# one with a backslash, or a single quote that starts a word (a quote in
# one, "doesn't", is text); a backtick is text, escaped. A $(...) may take
# long: Ctrl+C ends it with the line, as at any prompt.
__aish_expanding() {
	local __aish_x __aish_s
	[[ $__aish_t == *\\* ]] && return
	[[ $__aish_t == \'* || $__aish_t == *[^[:alnum:]]\'* ]] && return
	[[ $__aish_t == *'$('* ]] && zle -R 'expanding…'
	__aish_x=$(__aish_expand </dev/null 2>/dev/null)
	__aish_s=$?
	[[ $__aish_t == *'$('* ]] && zle -R ''
	((__aish_s == 0)) && [[ $__aish_x == *. ]] && typeset -g __aish_t=${__aish_x%.}
	return 0
}

# __aish_expand prints __aish_t expanded and a dot after it: the command
# substitution takes the newlines at the end, not the dot. $? in the text is
# the code of the user's last command; $1 is no word of it.
__aish_expand() {
	local __aish_e
	__aish_e=${__aish_t//\`/\\\`}
	set --
	__aish_status "$__aish_rc"
	print -rn -- "${(e)__aish_e}."
}

__aish_status() { return $1; }

# __aish_request rewrites the line into a request.
__aish_request() {
	emulate -L zsh -o extendedglob
	__aish_t=${${__aish_t##[[:space:]]#}%%[[:space:]]#}
	if [[ -z $__aish_t ]]; then
		BUFFER=
	else
		__aish_req=$__aish_t
		BUFFER='__aish_ask "$__aish_req"'
		__aish_redraw=1
		__aish_typed=${${__aish_buf##[[:space:]]#}%%[[:space:]]#}
	fi
	CURSOR=$#BUFFER
	__aish_ps0=
}

__aish_mark() {
	__aish_ps0=$'\e]6973;'"$__aish_nonce;cmd-start;${__aish_buf//[$'\a\e']/}"$'\a'
}

__aish_preexec() {
	[[ -n $__aish_ps0 ]] && print -rn -- "$__aish_ps0"
	typeset -g __aish_ps0=
	return 0
}

# The rewritten line stays out of history; __aish_ask puts what was typed
# there instead.
__aish_addhistory() {
	[[ $1 == '__aish_ask "$__aish_req"'* ]] && return 1
	return 0
}

# __aish_dump prints the shell's state for the proxy to save with the
# session (internal/shellstate parses it): `typeset -p`, NUL, the names of
# the special parameters, NUL, the functions as name NUL body NUL, NUL, the
# aliases as name NUL value NUL (global ones named "-g NAME", suffix ones
# "-s NAME"), NUL, then the options as name NUL on|off. Builtins only: it
# runs at every prompt. typeset -p comes before any local of its own.
__aish_dump() {
	typeset -p
	print -n '\0'
	local -a __aish_o
	__aish_o=("${(@kv)options[@]}")
	emulate -L zsh
	local __aish_n __aish_v
	print -rn -- ${(k)parameters[(R)*special*]}
	print -n '\0'
	for __aish_n in ${(ok)functions}; do
		[[ $__aish_n == _* ]] && continue
		print -rn -- "$__aish_n"$'\0'"${functions[$__aish_n]}"$'\0'
	done
	print -n '\0'
	for __aish_n __aish_v in "${(@kv)aliases}"; do
		print -rn -- "$__aish_n"$'\0'"$__aish_v"$'\0'
	done
	for __aish_n __aish_v in "${(@kv)galiases}"; do
		print -rn -- "-g $__aish_n"$'\0'"$__aish_v"$'\0'
	done
	for __aish_n __aish_v in "${(@kv)saliases}"; do
		print -rn -- "-s $__aish_n"$'\0'"$__aish_v"$'\0'
	done
	print -n '\0'
	print -rn -- "${(@pj:\0:)__aish_o}"
	return 0
}

__aish_precmd() {
	typeset -g __aish_rc=$?
	if [[ -n ${AISH_RUN-} ]]; then
		# The state the shell starts with, which a session's changes are
		# measured against; then the session `aish resume` switched to.
		if [[ -z $__aish_based ]]; then
			typeset -g __aish_based=1
			__aish_dump >|"$AISH_RUN/state.base"
		fi
		if [[ -s $AISH_RUN/restore.bash ]]; then
			# The options the script keeps aside while it is read: its
			# own, not a global of this function's.
			local -a __aish_so
			builtin source "$AISH_RUN/restore.bash"
			: >|"$AISH_RUN/restore.bash"
		fi
		# Written before cmd-end: the proxy reads it when the marker arrives.
		__aish_dump >|"$AISH_RUN/state"
	fi
	printf '\e]6973;%s;cmd-end;%s;%s\a' "$__aish_nonce" "$__aish_rc" "${PWD//[$'\a\e']/}"
	typeset -g __aish_ps0=
	return 0
}

# __aish_unecho replaces the `__aish_ask "$__aish_req"` line zle has left on
# the screen with the request, the prompt's % (or #, $, ❯, >) turned into
# ?: "host? text". The prompt is expanded with the user's options, %? the
# code of the user's last command.
__aish_unecho() {
	local __aish_p
	__aish_status "$__aish_rc"
	__aish_p=${(%%)PS1}
	__aish_unecho_draw "$1" "$__aish_p"
}

__aish_unecho_draw() {
	emulate -L zsh -o extendedglob
	local p vis c rows w cols line
	p=${2##*$'\n'}
	# What the escape sequences of the prompt take of no column.
	vis=${p//$'\e'\[[0-9;:?<=>]#[ -\/]#[@-~]/}
	vis=${vis//$'\e'\][^$'\a\e']#($'\a'|$'\e\\')/}
	vis=${vis//$'\e'?/}
	cols=${COLUMNS:-80}
	((cols > 0)) || cols=80
	line='__aish_ask "$__aish_req"'
	w=$((${(m)#vis} + ${#line}))
	rows=$(((w - 1) / cols + 1))
	((rows > 0)) || rows=1
	# The echo's first row is erased by itself: erase below from the
	# top-left corner is a clear screen to tmux, which keeps the screen, the
	# echo with it, in its history.
	for c in '%' '#' '$' '❯' '>'; do
		if [[ $p == *"$c"* ]]; then
			printf '\e[%dA\r\e[K\e[B\e[J\e[A%s?%s%s\n' "$rows" "${p%"$c"*}" "${p##*"$c"}" "$1"
			return
		fi
	done
	printf '\e[%dA\r\e[K\e[B\e[J\e[A%s\e[2m?\e[0m %s\n' "$rows" "$p" "$1"
}

__aish_ask() {
	# Before the locals: the local __aish_rc would hide the global one
	# __aish_unecho draws the prompt's %? with.
	if [[ ${__aish_redraw-} == 1 ]]; then
		typeset -g __aish_redraw=0
		__aish_unecho "$1"
	fi
	local __aish_q __aish_id __aish_cmd __aish_rc
	__aish_q=$1
	# $1 keeps the text for the agent's commands; the global would keep it
	# after the request.
	typeset -g __aish_req=
	print -rs -- "${__aish_typed:-$__aish_q}"
	typeset -g __aish_typed=

	printf '\e]6973;%s;ask-start\a' "$__aish_nonce"
	"$AISH_BIN" agent start -- "$__aish_q" || return
	while [[ -s $AISH_RUN/next.cmd ]]; do
		IFS= read -r __aish_id <"$AISH_RUN/next.id" || :
		IFS= read -r -d '' __aish_cmd <"$AISH_RUN/next.cmd" || :
		: >|"$AISH_RUN/next.cmd"
		__aish_rc=${__aish_cmd//[$'\a\e']/}
		printf '\e]6973;%s;agent-start;%s;%s\a' "$__aish_nonce" "$__aish_id" "${__aish_rc:0:1000}"
		eval "$__aish_cmd" </dev/null
		__aish_rc=$?
		printf '\e]6973;%s;agent-end;%s;%s;%s\a' "$__aish_nonce" "$__aish_id" "$__aish_rc" "${PWD//[$'\a\e']/}"
		"$AISH_BIN" agent resume "$__aish_id" "$__aish_rc" || break
	done
}

zle -A accept-line __aish_accept_prev
zle -N accept-line __aish_accept
preexec_functions+=(__aish_preexec)
precmd_functions+=(__aish_precmd)
zshaddhistory_functions+=(__aish_addhistory)

options+=("${(@kv)__aish_opts[@]}")
\unset __aish_opts
