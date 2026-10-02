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
#   fold-start;<title>, fold-end around a tool's live output (printed by aish)
#   agent-col;<col>;<long>       the agent left its command's line open at <col>
#                                for the proxy's status (printed by aish)
#
# The proxy folds long agent output; Ctrl+O (handled by the proxy) shows it.

[[ $- == *i* ]] || return 0
[[ -n ${__aish_loaded-} ]] && return 0
__aish_loaded=1

# A file, not the environment: every command would inherit that.
__aish_nonce=
[[ -n ${AISH_RUN-} ]] && IFS= read -r __aish_nonce <"$AISH_RUN/nonce"

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

	# A request to the assistant starts with a capital letter. Anything else,
	# typos included, is bash's.
	if [[ ${trimmed:0:1} == [[:upper:]] ]] && ! __aish_is_command "$trimmed"; then
		__aish_to_llm "$trimmed"
	else
		__aish_mark
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
	printf '\e]6973;%s;cmd-end;%s;%s\a' "$__aish_nonce" "$__aish_rc" "$PWD"
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
		printf '\e]6973;%s;agent-end;%s;%s;%s\a' "$__aish_nonce" "$__aish_id" "$__aish_rc" "$PWD"
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
