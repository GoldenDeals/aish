# aish bash integration. Installed after ~/.bashrc by the aish PTY proxy.
#
# Enter is rebound to: __aish_route (bind -x, may rewrite the line) + accept-line.
# Text that is not a command becomes `__aish_ask '<text>'`, so bash never
# parses the natural language itself.
#
# Markers, OSC 6973;<nonce>;<kind>[;<payload>] BEL, are stripped by the proxy
# before they reach the terminal. The nonce is the session's, from
# $AISH_RUN/nonce: a sequence with any other one is output like any text.
#   cmd-start;<command>          PS0, right before a user command runs
#   cmd-end;<rc>;<cwd>           PROMPT_COMMAND, before each primary prompt;
#                                $AISH_RUN/state holds the shell's state by then
#   ask-start                    a request to the assistant begins
#   agent-start;<id>;<command>   before a command requested by the agent
#   agent-end;<id>;<rc>;<cwd>    after it
#
# The agent itself runs in the proxy: `aish agent start|resume` only carry
# the request there and wait. The proxy folds long agent output; Ctrl+O
# (handled by the proxy) shows it.

[[ $- == *i* ]] || return 0
[[ -n ${__aish_loaded-} ]] && return 0
__aish_loaded=1

# A file, not the environment: every command would inherit that.
__aish_nonce=
[[ -n ${AISH_RUN-} ]] && IFS= read -r __aish_nonce <"$AISH_RUN/nonce"

# The config's [route], key=value lines from the proxy. Without the file,
# the rule from before it: a capital letter, nothing else.
__aish_route_capital=true __aish_route_not_found=false __aish_route_suffix= __aish_route_min_words=2
if [[ -n ${AISH_RUN-} && -f $AISH_RUN/route ]]; then
	while IFS= read -r __aish_l; do
		case ${__aish_l%%=*} in
		capital | not_found | suffix | min_words) printf -v "__aish_route_${__aish_l%%=*}" %s "${__aish_l#*=}" ;;
		esac
	done <"$AISH_RUN/route"
	unset __aish_l
fi

[[ -n ${AISH_TOOLS_PATH-} ]] && PATH="$AISH_TOOLS_PATH:$PATH"
type -P aish >/dev/null 2>&1 || aish() { "$AISH_BIN" "$@"; }

__aish_fresh=1   # 1 while readline is at the primary prompt (not PS2)
__aish_buf=      # full text of the command being entered (multi-line aware)
__aish_ps0=      # marker emitted by PS0, set only for user commands

__aish_route() {
	local line=$READLINE_LINE
	if [[ $__aish_fresh != 1 ]]; then
		# Continuation line (PS2): part of a command already routed to bash.
		__aish_buf+=$'\n'$line
		__aish_mark
		return
	fi
	__aish_fresh=0
	__aish_buf=$line
	__aish_ps0=
	__aish_hint=

	local trimmed=${line#"${line%%[![:space:]]*}"}
	[[ -z $trimmed ]] && return

	case $trimmed in
	'?'*)
		__aish_to_llm "${trimmed#\?}"
		return
		;;
	'@'*)
		# Starts with a file mention: "@main.go what is this?"
		__aish_to_llm "$trimmed"
		return
		;;
	'!'*)
		# Forced bash. A bare `!` keeps bash history expansion (`!!`, `!$`).
		case $trimmed in '!!'* | '!$'* | '!-'* | '!'[0-9]*) ;; *)
			READLINE_LINE=${trimmed#!}
			READLINE_POINT=${#READLINE_LINE}
			__aish_buf=$READLINE_LINE
			;;
		esac
		__aish_mark
		return
		;;
	esac

	# A skill typed as a command or as /name goes to the assistant as
	# `/name args` (agent/skillmention.go); any other command is bash's.
	# A line that is no command is a request by [route]: it starts with a
	# capital letter, ends with the suffix, or is words, not shell, that
	# bash would only answer with "command not found".
	local w=${trimmed%%[[:space:]]*} end=${trimmed%"${trimmed##*[![:space:]]}"}
	if [[ $w == /* ]] && __aish_is_skill "${w#/}"; then
		__aish_to_llm "$trimmed"
	elif __aish_is_command "$trimmed"; then
		__aish_mark
	elif __aish_is_skill "$w"; then
		__aish_to_llm "/$trimmed"
	elif [[ $__aish_route_capital == true && ${trimmed:0:1} == [[:upper:]] ]] ||
		[[ -n $__aish_route_suffix && $end == *"$__aish_route_suffix" ]]; then
		__aish_to_llm "$trimmed"
	elif [[ $__aish_route_not_found == true ]] && __aish_is_prose "$trimmed"; then
		__aish_to_llm "$trimmed"
	else
		__aish_mark
		if [[ -z ${__aish_hinted-} ]] && __aish_is_prose "$trimmed"; then
			__aish_hint=1 __aish_hinted=1 # for command_not_found_handle, once
		fi
	fi
}

# __aish_is_command decides by the first word, the way bash itself would.
__aish_is_command() {
	local w=${1%%[[:space:]]*}
	w=${w%%[;|&<>()]*}
	[[ -z $w ]] && return 0 # starts with an operator: let bash complain
	case $w in
	*=*) return 0 ;;            # VAR=value cmd
	/* | ./* | ../* | '~'*) return 0 ;; # path
	'$'* | '`'* | '"'* | "'"* | '#'* | '{' | '[[' | '((' | '!') return 0 ;;
	esac
	type -t -- "$w" >/dev/null 2>&1
}

# __aish_is_skill: is there a skill named $1 here? The roots are those of
# skills.Find (internal/skills), by directory name; builtins only, since it
# runs on every Enter with an unknown first word.
__aish_is_skill() {
	local n=$1 d
	[[ $n =~ ^[A-Za-z0-9_-]{1,64}$ ]] || return 1
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
	[[ $1 == *[\|\&\;\<\>\(\)\$\`\\=]* ]] && return 1
	local n=$((__aish_route_min_words - 1))
	((n > 0)) || return 0
	local re="^[^[:space:]]+([[:space:]]+[^[:space:]]+){$n}"
	[[ $1 =~ $re ]]
}

# With not_found off such a line is bash's, and the first one gets a hint
# after "command not found". The handler from ~/.bashrc (pkgfile,
# command-not-found) still answers. This runs in a child: it can print,
# not change the shell.
if [[ $__aish_route_not_found != true ]]; then
	if declare -F command_not_found_handle >/dev/null; then
		__aish_f=$(declare -f command_not_found_handle)
		eval "__aish_cnf_prev${__aish_f#command_not_found_handle}"
		unset __aish_f
	fi
	command_not_found_handle() {
		local __aish_rc=127 __aish_sh=${0##*/}
		if declare -F __aish_cnf_prev >/dev/null; then
			__aish_cnf_prev "$@"
			__aish_rc=$?
		else
			printf '%s: %s: command not found\n' "${__aish_sh#-}" "$1" >&2
		fi
		[[ ${__aish_hint-} == 1 ]] && printf 'aish: looks like a question; prefix with ? to ask\n' >&2
		return $__aish_rc
	}
fi

__aish_to_llm() {
	local q=$1
	q=${q#"${q%%[![:space:]]*}"}
	if [[ -z $q ]]; then
		READLINE_LINE=
	else
		READLINE_LINE="__aish_ask ${q@Q}"
		__aish_redraw=1
		__aish_typed=${__aish_buf#"${__aish_buf%%[![:space:]]*}"}
	fi
	READLINE_POINT=${#READLINE_LINE}
	__aish_ps0=
}

__aish_mark() {
	__aish_ps0=$'\e]6973;'"$__aish_nonce;cmd-start;${__aish_buf//[$'\a\e']/}"$'\a'
}

# __aish_dump prints the shell's state for the proxy to save with the
# session (internal/bashstate parses it). Builtins only: it runs at every
# prompt, and a fork there would be felt.
__aish_dump() {
	local __aish_n
	declare -p
	printf '\0'
	declare -f
	printf '\0'
	for __aish_n in "${!BASH_ALIASES[@]}"; do
		printf '%s\0%s\0' "$__aish_n" "${BASH_ALIASES[$__aish_n]}"
	done
	printf '\0'
	set +o
	shopt -p
}

__aish_precmd() {
	local __aish_rc=$?
	if [[ -n ${AISH_RUN-} ]]; then
		# The state the shell starts with, which a session's changes are
		# measured against; then the session `aish resume` switched to.
		[[ -z ${__aish_based-} ]] && __aish_based=1 && __aish_dump >|"$AISH_RUN/state.base"
		if [[ -s $AISH_RUN/restore.bash ]]; then
			builtin source "$AISH_RUN/restore.bash"
			: >|"$AISH_RUN/restore.bash"
		fi
		# Written before cmd-end: the proxy reads it when the marker arrives.
		__aish_dump >|"$AISH_RUN/state"
	fi
	printf '\e]6973;%s;cmd-end;%s;%s\a' "$__aish_nonce" "$__aish_rc" "${PWD//[$'\a\e']/}"
	__aish_fresh=1
	__aish_ps0=
	return $__aish_rc
}

# __aish_unecho replaces the `__aish_ask '...'` line readline has echoed with
# what the user typed, the prompt's `$` (or `#`) turned into `?`:
# "user@host:~? text".
__aish_unecho() {
	local p=${PS1@P} vis
	p=${p##*$'\n'}
	vis=$p
	while [[ $vis == *$'\001'*$'\002'* ]]; do
		vis=${vis%%$'\001'*}${vis#*$'\002'}
	done
	p=${p//[$'\001\002']/}
	local line="__aish_ask ${1@Q}" cols=${COLUMNS:-80}
	local rows=$(((${#vis} + ${#line}) / cols + 1))
	local c
	for c in '$' '#'; do
		if [[ $p == *"$c"* ]]; then
			printf '\e[%dA\r\e[J%s?%s%s\n' "$rows" "${p%"$c"*}" "${p##*"$c"}" "$1"
			return
		fi
	done
	printf '\e[%dA\r\e[J%s\e[2m?\e[0m %s\n' "$rows" "$p" "$1"
}

__aish_ask() {
	local __aish_q=$1 __aish_id __aish_cmd __aish_rc
	if [[ ${__aish_redraw-} == 1 ]]; then
		__aish_redraw=0
		__aish_unecho "$__aish_q"
	fi
	# The rewritten line is kept out of history by HISTIGNORE; record what the
	# user typed instead.
	[[ -o history ]] && builtin history -s -- "${__aish_typed:-$__aish_q}"
	__aish_typed=

	printf '\e]6973;%s;ask-start\a' "$__aish_nonce"
	"$AISH_BIN" agent start -- "$__aish_q" || return
	while [[ -s $AISH_RUN/next.cmd ]]; do
		IFS= read -r __aish_id <"$AISH_RUN/next.id"
		IFS= read -r -d '' __aish_cmd <"$AISH_RUN/next.cmd"
		: >|"$AISH_RUN/next.cmd"
		__aish_rc=${__aish_cmd//[$'\a\e']/}
		printf '\e]6973;%s;agent-start;%s;%s\a' "$__aish_nonce" "$__aish_id" "${__aish_rc:0:1000}"
		eval "$__aish_cmd" </dev/null
		__aish_rc=$?
		printf '\e]6973;%s;agent-end;%s;%s;%s\a' "$__aish_nonce" "$__aish_id" "$__aish_rc" "${PWD//[$'\a\e']/}"
		"$AISH_BIN" agent resume "$__aish_id" "$__aish_rc" || break
	done
}

for __aish_km in emacs vi-insert; do
	bind -m "$__aish_km" -x '"\C-x\C-a": __aish_route'
	bind -m "$__aish_km" '"\C-x\C-b": accept-line'
	bind -m "$__aish_km" '"\C-m": "\C-x\C-a\C-x\C-b"'
	bind -m "$__aish_km" '"\C-j": "\C-x\C-a\C-x\C-b"'
done
unset __aish_km

HISTIGNORE="${HISTIGNORE:+$HISTIGNORE:}__aish_ask *"
PS0='${__aish_ps0}'"${PS0-}"
if [[ $(declare -p PROMPT_COMMAND 2>/dev/null) == "declare -a"* ]]; then
	PROMPT_COMMAND=(__aish_precmd "${PROMPT_COMMAND[@]}")
else
	PROMPT_COMMAND="__aish_precmd${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
fi
