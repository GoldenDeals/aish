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
__aish_route_expand=true
if [[ -n ${AISH_RUN-} && -f $AISH_RUN/route ]]; then
	while IFS= read -r __aish_l; do
		case ${__aish_l%%=*} in
		capital | not_found | suffix | min_words | expand) printf -v "__aish_route_${__aish_l%%=*}" %s "${__aish_l#*=}" ;;
		esac
	done <"$AISH_RUN/route"
	unset __aish_l
fi

[[ -n ${AISH_TOOLS_PATH-} ]] && PATH="$AISH_TOOLS_PATH:$PATH"
type -P aish >/dev/null 2>&1 || aish() { "$AISH_BIN" "$@"; }

__aish_fresh=1   # 1 while readline is at the primary prompt (not PS2)
__aish_buf=      # full text of the command being entered (multi-line aware)
__aish_ps0=      # marker emitted by PS0, set only for user commands

# The locals here, in __aish_to_llm and __aish_expanding are __aish_ names:
# the request __aish_expand expands sees them, and $line means the user's.
# __aish_rc is the code of the user's last command, for $? in the request.
__aish_route() {
	local __aish_rc=$? __aish_line=$READLINE_LINE
	if [[ $__aish_fresh != 1 ]]; then
		# Continuation line (PS2): part of a command already routed to bash.
		__aish_buf+=$'\n'$__aish_line
		__aish_mark
		return
	fi
	__aish_fresh=0
	__aish_buf=$__aish_line
	__aish_ps0=
	__aish_hint=

	local __aish_trim=${__aish_line#"${__aish_line%%[![:space:]]*}"}
	[[ -z $__aish_trim ]] && return

	case $__aish_trim in
	'?'*)
		__aish_to_llm "${__aish_trim#\?}" raw
		return
		;;
	'@'*)
		# Starts with a file mention: "@main.go what is this?"
		__aish_to_llm "$__aish_trim"
		return
		;;
	'!'*)
		# Forced bash. A bare `!` keeps bash history expansion (`!!`, `!$`).
		case $__aish_trim in '!!'* | '!$'* | '!-'* | '!'[0-9]*) ;; *)
			READLINE_LINE=${__aish_trim#!}
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
	local __aish_w=${__aish_trim%%[[:space:]]*} __aish_end=${__aish_trim%"${__aish_trim##*[![:space:]]}"}
	if [[ $__aish_w == /* ]] && __aish_is_skill "${__aish_w#/}"; then
		__aish_to_llm "$__aish_trim"
	elif __aish_is_command "$__aish_trim"; then
		__aish_mark
	elif __aish_is_skill "$__aish_w"; then
		__aish_to_llm "/$__aish_trim"
	elif [[ $__aish_route_capital == true && ${__aish_trim:0:1} == [[:upper:]] ]] ||
		[[ -n $__aish_route_suffix && $__aish_end == *"$__aish_route_suffix" ]]; then
		__aish_to_llm "$__aish_trim"
	elif [[ $__aish_route_not_found == true ]] && __aish_is_prose "$__aish_trim"; then
		__aish_to_llm "$__aish_trim"
	else
		__aish_mark
		if [[ -z ${__aish_hinted-} ]] && __aish_is_prose "$__aish_trim"; then
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

# __aish_expand prints $1 with $VAR, ${...} and $(...) expanded as in a
# here-document: quotes are text there and work inside $(...). Backticks
# stay text, the backslashes before them too, and "\$" is a dollar. It
# fails when the text does not parse, and the caller keeps it as typed. Call
# it in a subshell: ${V:=x} and $((n++)) assign.
__aish_expand() {
	local __aish_r=$1 __aish_t= __aish_s __aish_o=
	# A line of the text that is the delimiter would end the here-document,
	# and the lines after it would run.
	[[ $__aish_r == *__aish_eof* ]] && return 1
	while [[ $__aish_r == *'`'* ]]; do
		__aish_s=${__aish_r%%'`'*}
		# Doubled, a backslash before the backtick cannot take the one
		# that escapes it.
		__aish_t+=$__aish_s${__aish_s##*[!\\]}'\`'
		__aish_r=${__aish_r#*'`'}
	done
	set -- # $1 is the text here, not the user's
	# $? too: the code __aish_route found, not that of `set`. A function,
	# not (exit N): that would fork once more.
	__aish_status "${__aish_rc:-0}"
	# The dot: a backslash at the end would join the delimiter's line to it.
	eval "IFS= read -r -d '' __aish_o <<__aish_eof || :
$__aish_t$__aish_r.
__aish_eof
" 2>/dev/null
	[[ $__aish_o == *.$'\n' ]] || return 1
	printf '%s' "${__aish_o%.$'\n'}"
}

__aish_status() { return "$1"; }

# __aish_expanding sets __aish_x, a local of its caller, to $1 expanded,
# and fails as __aish_expand does. A $(...) may take long: a dim
# "expanding…" stands meanwhile for the line bash erased for bind -x, and
# Ctrl+C ends only the substitution. Untrapped, it would throw the shell
# out of bind -x, and the rest of the Enter macro would accept an empty
# line at a prompt more. __aish_intr keeps the code, 130 or that of the
# signal that killed the substitution, for __aish_ask to return unasked.
__aish_expanding() {
	if [[ $1 != *'$('* ]]; then
		__aish_x=$(__aish_expand "$1" </dev/null)
		return
	fi
	local __aish_p __aish_s
	__aish_p=$(trap -p INT)
	trap '__aish_intr=130' INT
	printf '\e[2mexpanding…\e[0m\r' >&2
	__aish_x=$(__aish_expand "$1" </dev/null)
	__aish_s=$?
	printf '\e[K' >&2
	if [[ -n $__aish_p ]]; then eval "$__aish_p"; else trap - INT; fi
	((__aish_s > 128)) && __aish_intr=$__aish_s
	[[ -z ${__aish_intr-} ]] || return 1
	return $__aish_s
}

# __aish_to_llm rewrites the line into a request. The text is expanded
# here, before __aish_req, for the screen, the journal and the model to
# get the same one; history gets what was typed (__aish_typed). A second
# argument, from the ? prefix: the text goes as typed. Whitespace around
# it goes: a paste ending in a newline would leave an empty line on the
# screen and send the newline. The line itself is a short one that reads
# the text from __aish_req: readline draws it anew before accept-line, and
# a line taller than the screen would push its top, `__aish_ask '...`,
# into the scrollback, out of __aish_unecho's reach.
__aish_to_llm() {
	local __aish_t=$1 __aish_x
	__aish_t=${__aish_t#"${__aish_t%%[![:space:]]*}"}
	__aish_t=${__aish_t%"${__aish_t##*[![:space:]]}"}
	if [[ -z ${2-} && $__aish_route_expand == true && $__aish_t == *'$'* ]]; then
		__aish_expanding "$__aish_t" && __aish_t=$__aish_x
	fi
	if [[ -z $__aish_t ]]; then
		READLINE_LINE=
	else
		__aish_req=$__aish_t
		READLINE_LINE='__aish_ask "$__aish_req"'
		__aish_redraw=1
		__aish_typed=${__aish_buf#"${__aish_buf%%[![:space:]]*}"}
		__aish_typed=${__aish_typed%"${__aish_typed##*[![:space:]]}"}
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
	__aish_intr=
	return $__aish_rc
}

# __aish_rows sets __aish_n, a local of its caller, to the rows text $1 takes
# on a terminal $2 columns wide. A wide character, East Asian or emoji, takes
# two columns, and goes to the next row when one is left, as readline puts it
# there. The ranges stand in for wcwidth, which is a fork away; they are
# UTF-8 bytes, which match no ASCII character in any locale, as $'\u…' may
# stay an escape in the C locale. Text is taken a run of narrow or wide
# characters at a time: a loop over characters takes seconds on a long one.
__aish_rows() {
	# U+1100-115F, U+2E80-A4CF, U+AC00-D7A3, U+F900-FAFF, U+FE30-FE4F,
	# U+FF00-FF60, U+FFE0-FFE6, U+1F300-1FAFF, U+20000-3FFFD.
	local __aish_w=$'\xe1\x84\x80-\xe1\x85\x9f\xe2\xba\x80-\xea\x93\x8f\xea\xb0\x80-\xed\x9e\xa3\xef\xa4\x80-\xef\xab\xbf\xef\xb8\xb0-\xef\xb9\x8f\xef\xbc\x80-\xef\xbd\xa0\xef\xbf\xa0-\xef\xbf\xa6\xf0\x9f\x8c\x80-\xf0\x9f\xab\xbf\xf0\xa0\x80\x80-\xf0\xbf\xbf\xbd'
	local __aish_s=$1 __aish_r __aish_x=0 __aish_f __aish_p=$(($2 > 1 ? $2 / 2 : 1)) __aish_g=
	__aish_n=1
	# Without globasciiranges a range is one of the locale's collation.
	shopt -q globasciiranges || __aish_g=1
	shopt -s globasciiranges
	while :; do
		__aish_r=${__aish_s%%[$__aish_w]*}
		__aish_s=${__aish_s:${#__aish_r}}
		__aish_x=$((__aish_x + ${#__aish_r}))
		if ((__aish_x > $2)); then
			__aish_n=$((__aish_n + (__aish_x - 1) / $2))
			__aish_x=$(((__aish_x - 1) % $2 + 1))
		fi
		[[ -n $__aish_s ]] || break
		# As many wide characters as fit in the row, the rest __aish_p a row.
		__aish_r=${__aish_s%%[!$__aish_w]*}
		__aish_s=${__aish_s:${#__aish_r}}
		__aish_f=$((($2 - __aish_x) / 2))
		if ((${#__aish_r} <= __aish_f)); then
			__aish_x=$((__aish_x + 2 * ${#__aish_r}))
		else
			__aish_f=$((${#__aish_r} - __aish_f - 1))
			__aish_n=$((__aish_n + 1 + __aish_f / __aish_p))
			__aish_x=$(((__aish_f % __aish_p + 1) * 2))
		fi
	done
	[[ -z $__aish_g ]] || shopt -u globasciiranges
}

# __aish_unecho replaces the `__aish_ask "$__aish_req"` line readline has
# echoed with the request, the prompt's `$` (or `#`) turned into `?`:
# "user@host:~? text". The echo is that line, not what was typed: bind -x
# erases the line typed and readline draws the one __aish_to_llm left.
__aish_unecho() {
	local p=${PS1@P} vis
	p=${p##*$'\n'}
	vis=$p
	while [[ $vis == *$'\001'*$'\002'* ]]; do
		vis=${vis%%$'\001'*}${vis#*$'\002'}
	done
	p=${p//[$'\001\002']/}
	local line='__aish_ask "$__aish_req"' cols=${COLUMNS:-80} __aish_n
	# Readline leaves the cursor under the echo's last row, a full one too.
	__aish_rows "$vis$line" "$cols"
	local rows=$__aish_n
	local c
	# The echo's first row is erased by itself: erase below from the
	# top-left corner is a clear screen to tmux, which keeps the screen, the
	# echo with it, in its history.
	for c in '$' '#'; do
		if [[ $p == *"$c"* ]]; then
			printf '\e[%dA\r\e[K\e[B\e[J\e[A%s?%s%s\n' "$rows" "${p%"$c"*}" "${p##*"$c"}" "$1"
			return
		fi
	done
	printf '\e[%dA\r\e[K\e[B\e[J\e[A%s\e[2m?\e[0m %s\n' "$rows" "$p" "$1"
}

__aish_ask() {
	local __aish_q=$1 __aish_id __aish_cmd __aish_rc
	# $1 keeps the text for the agent's commands; the global would keep it
	# after the request.
	unset -v __aish_req
	if [[ ${__aish_redraw-} == 1 ]]; then
		__aish_redraw=0
		__aish_unecho "$__aish_q"
	fi
	# The rewritten line is kept out of history by HISTIGNORE; record what the
	# user typed instead.
	[[ -o history ]] && builtin history -s -- "${__aish_typed:-$__aish_q}"
	__aish_typed=
	# Ctrl+C cut its expansion short: on the screen and in history, unsent.
	if [[ -n ${__aish_intr-} ]]; then
		__aish_rc=$__aish_intr __aish_intr=
		return "$__aish_rc"
	fi

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

# Ctrl+V (or Ctrl+Q) before a paste, a habit where the terminal pastes with
# Ctrl+Shift+V: quoted-insert would take the paste's ESC for the character
# and the rest as typed: the line would be ^[[200~text~, and a newline in
# the text would run it. \C-x\C-q is quoted-insert with \e[200~ under it:
# the paste goes in whole, as without Ctrl+V, and Tab, Esc or another key
# after it is the character still. The key gets there by a macro: readline
# waits for the key after a macro's last one as long as it takes, and the
# paste may come a second after Ctrl+V. Under the key itself the sequence
# would end after keyseq-timeout, half a second. A key the user bound to
# something else or bound sequences under stays his. Once, at load: $(...)
# forks.
for __aish_km in emacs vi-insert; do
	__aish_b=$'\n'$(bind -m "$__aish_km" -p; bind -m "$__aish_km" -s; bind -m "$__aish_km" -X)$'\n'
	[[ $__aish_b == *$'\n"\\e[200~": bracketed-paste-begin\n'* && $__aish_b != *$'\n"\\C-x\\C-q'* ]] || continue
	__aish_q=
	for __aish_k in '\C-q' '\C-v'; do
		if [[ $__aish_b == *$'\n"'"$__aish_k"$'": quoted-insert\n'* && $__aish_b != *$'\n"'"$__aish_k"[!\"]* ]]; then
			bind -m "$__aish_km" "\"$__aish_k\": \"\\C-x\\C-q\""
			__aish_q=1
		fi
	done
	if [[ -n $__aish_q ]]; then
		bind -m "$__aish_km" '"\C-x\C-q": quoted-insert'
		bind -m "$__aish_km" '"\C-x\C-q\e[200~": bracketed-paste-begin'
	fi
done
unset __aish_km __aish_k __aish_b __aish_q

HISTIGNORE="${HISTIGNORE:+$HISTIGNORE:}__aish_ask *"
PS0='${__aish_ps0}'"${PS0-}"
if [[ $(declare -p PROMPT_COMMAND 2>/dev/null) == "declare -a"* ]]; then
	PROMPT_COMMAND=(__aish_precmd "${PROMPT_COMMAND[@]}")
else
	PROMPT_COMMAND="__aish_precmd${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
fi
