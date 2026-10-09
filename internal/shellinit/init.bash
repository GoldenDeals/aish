# aish bash integration. Installed after ~/.bashrc by the aish PTY proxy.
#
# Enter is rebound to: __aish_route (bind -x, may rewrite the line) + accept-line.
# Text that is not a command goes to the global __aish_req and the line becomes
# `__aish_ask "$__aish_req"`, so bash never parses the natural language itself.
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
#
# These markers, the files of $AISH_RUN (nonce, route, next.cmd, next.id,
# esc, state.base, state, restore.bash) and the calls of `aish agent` are
# the contract between the proxy and any shell: init.zsh keeps it too.
# esc is "<call id> <code>" when Esc stopped the agent's command: the shell
# ends it with agent-end and that code, and resumes the agent.

[[ $- == *i* ]] || return 0
[[ -n ${__aish_loaded-} ]] && return 0
__aish_loaded=1

# A file, not the environment: every command would inherit that.
__aish_nonce=
[[ -n ${AISH_RUN-} ]] && IFS= read -r __aish_nonce <"$AISH_RUN/nonce"

# The config's [route], key=value lines from the proxy. Without the file,
# the rule from before it: a capital letter, nothing else.
__aish_route_capital=true __aish_route_not_found=false __aish_route_suffix= __aish_route_min_words=2
__aish_route_expand=false
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
__aish_cur=      # call id of the agent's command running, see __aish_run
__aish_asked=    # the text of the request in progress, see __aish_escaped
__aish_spawn=    # NAME of a line "&NAME text", for __aish_ask

# Every local in this file is declared bare and assigned apart: under the
# user's set -k, `local x=v` puts x=v in the environment of local, which
# then declares nothing. An assignment on its own is one under -k too.

# The locals here, in __aish_to_llm and __aish_expanding are __aish_ names:
# the request __aish_expand expands sees them, and $line means the user's.
# __aish_rc is the code of the user's last command, for $? in the request:
# a global, set first thing, as local would reset $?.
__aish_route() {
	__aish_rc=$?
	local __aish_line
	__aish_line=$READLINE_LINE
	[[ -z ${__aish_held-} ]] || __aish_hold accept-line
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
	__aish_spawn=

	local __aish_trim
	__aish_trim=${__aish_line#"${__aish_line%%[![:space:]]*}"}
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
	'&'[A-Za-z0-9_-]*)
		# "&reviewer check the diff": the subagent, in the background, on
		# the text after its name (__aish_spawning). To bash & and a word
		# are a syntax error; &>file is a redirection, and stays its.
		__aish_spawn=${__aish_trim%%[[:space:]]*}
		__aish_spawn=${__aish_spawn#&}
		__aish_to_llm "${__aish_trim#&"$__aish_spawn"}"
		return
		;;
	'!'*)
		# Forced bash: `!cmd` runs cmd. History expansion (`!!`, `!$`) keeps
		# its `!`, and so does bash's own negation, `!` before a blank or
		# alone: `! [ -f keep ] && rm -rf dir` without it removes dir.
		case $__aish_trim in '!' | '!'[[:space:]]* | '!!'* | '!$'* | '!-'* | '!'[0-9]*) ;; *)
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
	local __aish_w __aish_end
	__aish_w=${__aish_trim%%[[:space:]]*} __aish_end=${__aish_trim%"${__aish_trim##*[![:space:]]}"}
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
	local w
	w=${1%%[[:space:]]*}
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
	local n d
	n=$1
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

# __aish_is_agent: is there a subagent named $1 here? The roots are those of
# subagent.Find (internal/subagent), by file name, as __aish_is_skill has
# those of skills.Find, and the farthest of them is aish's own subagents
# (builtin.go), which have no file.
__aish_is_agent() {
	local n d
	n=$1
	[[ $n =~ ^[A-Za-z0-9_-]{1,64}$ ]] || return 1
	for d in "$HOME/.claude" "${XDG_CONFIG_HOME:-$HOME/.config}/aish"; do
		__aish_agent_file "$d/agents/$n.md" && return 0
	done
	d=$PWD
	while [[ -n $d ]]; do
		__aish_agent_file "$d/.claude/agents/$n.md" && return 0
		d=${d%/*}
	done
	__aish_agent_file "/.claude/agents/$n.md" && return 0
	[[ $n == general-purpose || $n == Explore ]]
}

# __aish_agent_file: does the file $1 start with a frontmatter, as
# subagent.Find takes a subagent's to? A README beside them does not.
__aish_agent_file() {
	local __aish_l
	[[ -f $1 && -r $1 ]] || return 1
	__aish_l=
	IFS= read -r __aish_l <"$1" || [[ -n $__aish_l ]] || return 1
	__aish_l=${__aish_l#$'\xef\xbb\xbf'}
	__aish_l=${__aish_l#"${__aish_l%%[![:space:]]*}"}
	[[ ${__aish_l%"${__aish_l##*[![:space:]]}"} == --- ]]
}

# __aish_is_prose: is $1 words rather than shell — min_words of them or
# more, and none of | & ; < > ( ) $ ` \ =? Quotes are prose: "doesn't".
__aish_is_prose() {
	[[ $1 == *[\|\&\;\<\>\(\)\$\`\\=]* ]] && return 1
	local n re
	n=$((__aish_route_min_words - 1))
	((n > 0)) || return 0
	re="^[^[:space:]]+([[:space:]]+[^[:space:]]+){$n}"
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
		local __aish_rc __aish_sh
		__aish_rc=127 __aish_sh=${0##*/}
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

# __aish_split sets __aish_p, an array of __aish_body, to $1 cut before
# every ', $(, ${, $[ and $\<newline>: __aish_body walks the pieces, not the
# characters, as ${s:i:1} costs O(i) in a UTF-8 locale. The cutting is
# bytes, in the C locale: in UTF-8 each pattern match would convert the rest
# of the text anew. The cut is a byte the text lacks, left in __aish_d, a
# local of __aish_body; with none left it fails, and the text stays as
# typed. LC_ALL and IFS are its own: the text is expanded after it returns.
__aish_split() {
	local LC_ALL IFS __aish_c __aish_s
	local -
	LC_ALL=C
	set -f
	for __aish_d in $'\x1f' $'\x1e' $'\x1d' $'\x1c' ''; do
		[[ $1 != *"$__aish_d"* ]] && break
	done
	[[ -n $__aish_d ]] || return 1
	__aish_c=$1
	for __aish_s in "'" '$(' '${' '$[' '$\'$'\n'; do
		__aish_c=${__aish_c//"$__aish_s"/"$__aish_d$__aish_s"}
	done
	IFS=$__aish_d
	__aish_p=($__aish_c)
}

# __aish_body sets __aish_t, a local of __aish_expand, to the body of the
# here-document for $1. In a single quote of the top level a backslash goes
# before every \, $ and `; elsewhere a backtick is escaped and the
# backslashes before it doubled, and at the top level the other backslashes
# are doubled too, but for the last of an odd run before a $: the body gives
# them as typed, a backslash-newline joins no lines, and \$ is a dollar, as
# before. A run before a $ keeps its parity, so what is expanded stays the
# same. In $(...) and ${...} backslashes are shell syntax and stay. One
# pass: escaping the backticks after the quotes would double the
# backslashes in them once more.
#
# The quote is kept only where bash, reading the text as typed, is at the
# top level at both its ends: inside $(...) and ${...} quotes are shell
# syntax, and the text after a kept quote is read as it was, so nothing
# runs that did not. To know that, and where the top level is, the walk
# follows bash through $(...), ${...} and the quotes in them, a stack of
# what closes each (^ is the top level). What it cannot follow for sure
# ends it, and the rest is expanded as before: a comment, case (its a) has
# no pair) or a here-document in $(...), a line continuation there or after
# $ (it joins what bash reads), $'...', $[...], $$ before a quote or a
# substitution, a bare ( ) { in ${...}, and a single quote in ${...} in
# posix mode, which reads it as text. A text with no quote and no backslash
# needs no walk.
__aish_body() {
	local __aish_p __aish_g __aish_v __aish_d __aish_i __aish_j __aish_c __aish_a __aish_h __aish_s __aish_k __aish_e __aish_y __aish_o __aish_q
	__aish_t= __aish_g=() __aish_i=0 __aish_k=^ __aish_y= __aish_q=
	if [[ $1 == *[\'\\]* ]]; then
		__aish_split "$1" || return 1
	else
		__aish_p=("$1")
	fi
	[[ -o posix ]] && __aish_q=1
	# __aish_v has, for a piece that ends at the top level, its rest from
	# where the top level starts.
	__aish_v=("${__aish_p[0]}")
	for __aish_c in "${__aish_p[@]:1}"; do
		((++__aish_i))
		# __aish_y is the piece with the open quote, if any.
		if [[ -n $__aish_y && $__aish_c == "'"* && ${__aish_c:1:1} != [[:alnum:]] ]]; then
			if [[ $__aish_k == ^ ]]; then
				for ((__aish_j = __aish_y; __aish_j < __aish_i; __aish_j++)); do
					__aish_s=${__aish_p[__aish_j]//'\'/'\\'}
					__aish_s=${__aish_s//'$'/'\$'}
					__aish_p[__aish_j]=${__aish_s//'`'/'\`'}
				done
				__aish_g+=("$__aish_y" "$__aish_i")
				__aish_y= __aish_v[__aish_i]=$__aish_c
				continue
			fi
			__aish_y=
		fi
		__aish_o=1 # where the rest of the piece to walk starts
		case ${__aish_k: -1}${__aish_c:0:1} in
		"''") __aish_k=${__aish_k%?} ;;
		"'"?) continue ;;
		"^'")
			[[ -z $__aish_y && ${__aish_p[__aish_i - 1]: -1} != [[:alnum:]\\] ]] && __aish_y=$__aish_i
			__aish_v[__aish_i]=$__aish_c
			continue
			;;
		*)
			[[ $__aish_c == '$'[\[\\]* ]] && break
			# An odd run of backslashes before it makes its first
			# character text.
			__aish_e=
			if [[ ${__aish_p[__aish_i - 1]} == *'\' ]]; then
				__aish_a=${__aish_p[__aish_i - 1]##*[!\\]}
				((${#__aish_a} % 2)) && __aish_e=1
			fi
			if [[ -z $__aish_e ]]; then
				[[ ${__aish_p[__aish_i - 1]} == *'$' && ${__aish_k: -1}${__aish_c:0:1} != \"\' ]] && break
				case ${__aish_k: -1}${__aish_c:0:2} in
				[\)\}]\'*)
					[[ $__aish_k == *'}' && -n $__aish_q ]] && break
					__aish_k+="'"
					continue
					;;
				*'$(') __aish_k+=')' __aish_o=2 ;;
				*'${') __aish_k+='}' __aish_o=2 ;;
				esac
			fi
			;;
		esac
		if [[ $__aish_k == *[\^\'] ]]; then
			[[ $__aish_k == ^ ]] && __aish_v[__aish_i]=$__aish_c
			continue
		fi
		__aish_h=${__aish_c:__aish_o}
		while :; do
			case ${__aish_k: -1} in
			')')
				__aish_s=${__aish_h%%[\\\"\(\)]*}
				case $__aish_s in *[#]* | *'<<'* | *case*) break 2 ;; esac
				;;
			'}') __aish_s=${__aish_h%%[\\\"\(\)\{\}]*} ;;
			'"') __aish_s=${__aish_h%%[\\\"]*} ;;
			*) break ;;
			esac
			[[ $__aish_s == "$__aish_h" ]] && break
			__aish_h=${__aish_h:${#__aish_s}}
			case ${__aish_k: -1}${__aish_h:0:2} in
			')\'$'\n') break 2 ;;
			?'\'*) __aish_h=${__aish_h:1} ;;
			')"'* | '}"'*) __aish_k+='"' ;;
			')('*) __aish_k+=')' ;;
			'))'* | '}}'* | '""'*) __aish_k=${__aish_k%?} ;;
			'}'?*) break 2 ;;
			esac
			__aish_h=${__aish_h:1}
		done
		[[ $__aish_k == ^ ]] && __aish_v[__aish_i]=$__aish_h
	done
	# Backticks and backslashes out of the kept quotes: __aish_g holds where
	# each starts and where its closing piece is. Bytes, in the C locale, as
	# in __aish_split: in UTF-8 each replacement would convert the rest of the
	# text anew.
	if [[ $1 == *[\`\\]* ]]; then
		local LC_ALL
		LC_ALL=C
		# Bash looks for the delimiter of the here-document with the lines
		# joined at a backslash-newline, which stays one in $(...), in
		# ${...} and after the walk ended: a line that becomes it would end
		# the body early, and the lines after it would run.
		[[ ${1//'\'$'\n'/} == *__aish_eof* ]] && return 1
		__aish_g+=("${#__aish_p[@]}" 0)
		__aish_i=0
		for ((__aish_j = 0; __aish_j < ${#__aish_g[@]}; __aish_j += 2)); do
			for ((; __aish_i < __aish_g[__aish_j]; __aish_i++)); do
				__aish_h=${__aish_p[__aish_i]} __aish_a= __aish_c=
				[[ $__aish_h == *[\`\\]* ]] || continue
				if [[ -n ${__aish_v[__aish_i]+1} ]]; then
					__aish_c=${__aish_v[__aish_i]}
					__aish_h=${__aish_h:0:${#__aish_h}-${#__aish_c}}
					__aish_body_top
				fi
				if [[ $__aish_h == *'`'* ]]; then
					while [[ $__aish_h == *'`'* ]]; do
						__aish_s=${__aish_h%%'`'*}
						# Doubled, a backslash before the backtick cannot
						# take the one that escapes it.
						__aish_a+=$__aish_s${__aish_s##*[!\\]}'\`'
						__aish_h=${__aish_h#*'`'}
					done
				fi
				__aish_p[__aish_i]=$__aish_a$__aish_h$__aish_c
			done
			__aish_i=${__aish_g[__aish_j + 1]}
		done
	fi
	printf -v __aish_t %s "${__aish_p[@]}"
}

# __aish_body_top escapes __aish_c, a local of __aish_body, the top level of
# its piece __aish_i: the backslashes doubled, but for the last of an odd
# run before a $, and the backticks escaped. A piece ends where the next
# starts with ' or $: the $ is put back for the backslashes before it. The
# pairs go first, as bash reads them, to __aish_d, a byte the text lacks.
__aish_body_top() {
	local __aish_n
	__aish_n=
	[[ ${__aish_p[__aish_i + 1]-} == '$'* ]] && __aish_n='$'
	__aish_c+=$__aish_n
	if [[ $__aish_c == *'\'* ]]; then
		__aish_c=${__aish_c//'\\'/"$__aish_d"}
		__aish_c=${__aish_c//'\'/'\\'}
		__aish_c=${__aish_c//'\\$'/'\$'}
		__aish_c=${__aish_c//"$__aish_d"/'\\\\'}
	fi
	__aish_c=${__aish_c//'`'/'\`'}
	__aish_c=${__aish_c%"$__aish_n"}
}

# __aish_expand prints $1 with $VAR, ${...} and $(...) expanded as in a
# here-document, except in single quotes: what is in them stays as typed,
# the quotes too (__aish_body). A quote opens after the start or a character
# that is neither alphanumeric nor a backslash, so the apostrophe in don't
# opens none. Double quotes and backticks stay text, backslashes too, but
# "\$" is a dollar. It fails when the text does not parse, and the caller
# keeps it as typed. Call it in a subshell: ${V:=x} and $((n++)) assign.
__aish_expand() {
	local __aish_r __aish_t __aish_o
	__aish_r=$1 __aish_t= __aish_o=
	# A line of the text that is the delimiter would end the here-document,
	# and the lines after it would run; __aish_body fails for one joined at
	# a backslash-newline.
	[[ $__aish_r == *__aish_eof* ]] && return 1
	__aish_body "$__aish_r" || return 1
	set -- # $1 is the text here, not the user's
	# $? too: the code __aish_route found, not that of `set`. A function,
	# not (exit N): that would fork once more.
	__aish_status "${__aish_rc:-0}"
	# The dot: a backslash at the end would join the delimiter's line to it.
	eval "IFS= read -r -d '' __aish_o <<__aish_eof || :
$__aish_t.
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
	local __aish_t __aish_x
	__aish_more && return
	__aish_t=$1
	__aish_t=${__aish_t#"${__aish_t%%[![:space:]]*}"}
	__aish_t=${__aish_t%"${__aish_t##*[![:space:]]}"}
	if [[ -z ${2-} && $__aish_route_expand == true && $__aish_t == *'$'* ]]; then
		__aish_expanding "$__aish_t" && __aish_t=$__aish_x
	fi
	# &NAME without text goes on too: __aish_spawning tells what it lacks.
	if [[ -z $__aish_t && -z ${__aish_spawn-} ]]; then
		READLINE_LINE=
	else
		__aish_req=$__aish_t
		READLINE_LINE='__aish_ask "$__aish_req"'
		# Under the user's set -e a request that fails, 130 after Ctrl+C
		# among them, or a command of the agent's that fails would close the
		# shell. In a list neither does, and $? is still the request's code.
		# Only under set -e: the list keeps the user's ERR trap from them too.
		[[ $- != *e* ]] || READLINE_LINE+=' && :'
		__aish_redraw=1
		__aish_typed=${__aish_buf#"${__aish_buf%%[![:space:]]*}"}
		__aish_typed=${__aish_typed%"${__aish_typed##*[![:space:]]}"}
	fi
	READLINE_POINT=${#READLINE_LINE}
	__aish_ps0=
}

# __aish_more takes a line for the assistant that ends in an odd run of
# backslashes on to the next one, as bash does a command: the last
# backslash and the blanks before it go, a newline takes their place, and
# readline keeps the line, which Enter sends whole. Nothing of the text
# runs, nor is expanded, before then, and history gets it as one line.
# Readline goes on with the accept-line of Enter's macro: it is
# end-of-line, a no-op there, till __aish_route gives it back.
__aish_more() {
	local __aish_e
	__aish_e=${READLINE_LINE##*[!\\]}
	((${#__aish_e} % 2)) || return 1
	__aish_e=${READLINE_LINE%\\}
	READLINE_LINE=${__aish_e%"${__aish_e##*[![:blank:]]}"}$'\n'
	READLINE_POINT=${#READLINE_LINE}
	__aish_fresh=1 # the line is still the primary prompt's
	__aish_hold end-of-line
}

# __aish_hold binds \C-x\C-b, the accept-line of Enter's macro, to $1 in
# each keymap of the macro's. In vi command mode end-of-line is
# vi-append-eol: bash takes a command there on to a next line in insert
# mode, and so the line goes on.
__aish_hold() {
	local __aish_m __aish_f
	for __aish_m in "${__aish_kms[@]}"; do
		__aish_f=$1
		[[ $__aish_m != vi-command || $1 != end-of-line ]] || __aish_f=vi-append-eol
		bind -m "$__aish_m" "\"\\C-x\\C-b\": $__aish_f"
	done
	__aish_held=
	[[ $1 == accept-line ]] || __aish_held=1
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
	__aish_rc=$? # a global, as in __aish_route
	# The agent's command was cut short; by Esc, the request goes on here.
	if [[ -n ${__aish_cur-} ]]; then
		__aish_escaped
	fi
	__aish_asked=
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
	local __aish_w __aish_s __aish_r __aish_x __aish_f __aish_p __aish_g
	__aish_w=$'\xe1\x84\x80-\xe1\x85\x9f\xe2\xba\x80-\xea\x93\x8f\xea\xb0\x80-\xed\x9e\xa3\xef\xa4\x80-\xef\xab\xbf\xef\xb8\xb0-\xef\xb9\x8f\xef\xbc\x80-\xef\xbd\xa0\xef\xbf\xa0-\xef\xbf\xa6\xf0\x9f\x8c\x80-\xf0\x9f\xab\xbf\xf0\xa0\x80\x80-\xf0\xbf\xbf\xbd'
	__aish_s=$1 __aish_x=0 __aish_p=$(($2 > 1 ? $2 / 2 : 1)) __aish_g=
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

# __aish_modestr sets __aish_m, a local of its caller, to the mode string
# readline draws before the prompt's last line under show-mode-in-prompt,
# empty without it: emacs-mode-string, or in vi that of the keymap the line
# was accepted in, which bind -v still names after accept-line. bind -v
# prints the string as readline keeps it, \001 and \002 around what takes
# no columns included. A fork, once a request: no builtin but bind tells
# readline's variables.
__aish_modestr() {
	local __aish_v __aish_k
	__aish_m=
	__aish_v=$'\n'$(builtin bind -v 2>/dev/null)$'\n' || :
	[[ $__aish_v == *$'\nset show-mode-in-prompt on\n'* ]] || return 0
	if [[ $__aish_v == *$'\nset editing-mode emacs\n'* ]]; then
		__aish_k=emacs
	elif [[ $__aish_v == *$'\nset keymap vi-insert\n'* ]]; then
		__aish_k=vi-ins
	else
		__aish_k=vi-cmd
	fi
	__aish_k=$'\nset '$__aish_k'-mode-string '
	[[ $__aish_v == *"$__aish_k"* ]] || return 0
	__aish_m=${__aish_v#*"$__aish_k"}
	__aish_m=${__aish_m%%$'\n'*}
}

# __aish_unecho replaces the `__aish_ask "$__aish_req"` line readline has
# echoed with the request, the prompt's `$` (or `#`) turned into `?`:
# "user@host:~? text". The echo is that line, not what was typed: bind -x
# erases the line typed and readline draws the one __aish_to_llm left.
__aish_unecho() {
	local p vis
	p=${PS1-} # set -u: PS1 may be unset
	# $? in the prompt is what it was at the prompt the request was typed
	# at, the code __aish_route found, not that of local. In a list, a code
	# other than 0 neither runs the user's ERR trap nor exits under set -e.
	__aish_status "${__aish_rc:-0}" && :
	p=${p@P}
	p=${p##*$'\n'}
	# The mode string of show-mode-in-prompt is in the echo's rows, not in
	# the line drawn in its place.
	local __aish_m
	__aish_modestr
	vis=$__aish_m$p
	while [[ $vis == *$'\001'*$'\002'* ]]; do
		vis=${vis%%$'\001'*}${vis#*$'\002'}
	done
	p=${p//[$'\001\002']/}
	local line cols __aish_n
	line='__aish_ask "$__aish_req"' cols=${COLUMNS:-80}
	[[ $- != *e* ]] || line+=' && :' # as __aish_to_llm writes it
	# Readline leaves the cursor under the echo's last row, a full one too.
	__aish_rows "$vis$line" "$cols"
	local rows
	rows=$__aish_n
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
	# Before the locals: the local __aish_rc would hide the global one
	# __aish_unecho draws the prompt's $? with.
	if [[ ${__aish_redraw-} == 1 ]]; then
		__aish_redraw=0
		__aish_unecho "${__aish_spawn:+&$__aish_spawn${1:+ }}$1"
	fi
	local __aish_q __aish_rc __aish_a
	__aish_q=$1 __aish_a=${__aish_spawn-}
	# $1 keeps the text for the agent's commands; the global would keep it
	# after the request. __aish_asked keeps it for those the prompt runs
	# after Esc (__aish_escaped), till __aish_precmd.
	unset -v __aish_req
	__aish_spawn=
	# The rewritten line is kept out of history by HISTIGNORE; record what the
	# user typed instead.
	[[ -o history ]] && builtin history -s -- "${__aish_typed:-$__aish_q}"
	__aish_typed=
	# Ctrl+C cut its expansion short: on the screen and in history, unsent.
	if [[ -n ${__aish_intr-} ]]; then
		__aish_rc=$__aish_intr __aish_intr=
		return "$__aish_rc"
	fi
	if [[ -n $__aish_a ]]; then
		__aish_spawning "$__aish_a" "$__aish_q"
		return
	fi

	printf '\e]6973;%s;ask-start\a' "$__aish_nonce"
	"$AISH_BIN" agent start -- "$__aish_q" || return
	__aish_asked=$__aish_q
	__aish_run "$__aish_q"
}

# __aish_spawning starts subagent $1 in the background on $2, the text of a
# line "&NAME text": the proxy runs it, nothing of it goes to the session,
# and the first prompt after it is done tells so. A name no agents directory
# here has a file of, or no text, gets a hint, and nothing starts.
__aish_spawning() {
	if ! __aish_is_agent "$1"; then
		printf 'aish: no subagent %s here; aish agents lists them\n' "$1" >&2
		return 2
	fi
	if [[ -z $2 ]]; then
		printf 'aish: what is %s to do? &%s TEXT\n' "$1" "$1" >&2
		return 2
	fi
	"$AISH_BIN" agent spawn "$1" -- "$2"
}

# __aish_run runs the commands the agent leaves for the shell, $1 the text
# of the request for them, until it leaves none. __aish_cur is the call
# whose command runs, from before its agent-start to after its agent-end:
# Esc stops that command with SIGINT, which throws the whole line away, as
# Ctrl+C does, and __aish_escaped goes on with the request at the prompt.
__aish_run() {
	local __aish_id __aish_cmd __aish_rc
	while [[ -s $AISH_RUN/next.cmd ]]; do
		IFS= read -r __aish_id <"$AISH_RUN/next.id"
		# Read whole, to the end of the file: read fails there, and that is
		# no error to run the user's ERR trap for.
		IFS= read -r -d '' __aish_cmd <"$AISH_RUN/next.cmd" || :
		: >|"$AISH_RUN/next.cmd"
		__aish_rc=${__aish_cmd//[$'\a\e']/}
		__aish_cur=$__aish_id
		printf '\e]6973;%s;agent-start;%s;%s\a' "$__aish_nonce" "$__aish_id" "${__aish_rc:0:1000}"
		eval "$__aish_cmd" </dev/null
		__aish_rc=$?
		printf '\e]6973;%s;agent-end;%s;%s;%s\a' "$__aish_nonce" "$__aish_id" "$__aish_rc" "${PWD//[$'\a\e']/}"
		__aish_cur=
		"$AISH_BIN" agent resume "$__aish_id" "$__aish_rc" || break
	done
}

# __aish_escaped goes on with the request at the prompt after Esc stopped
# the agent's command: the proxy wrote "<call id> <code>" to $AISH_RUN/esc,
# then sent SIGINT. No trap keeps the line from that, not in a loop of the
# shell's own: it is gone, __aish_ask with it, as after Ctrl+C. Here goes
# what __aish_run would have done: agent-end with that code, `agent
# resume`, and the commands the agent leaves after it. $? stays 130: bash
# gives it back after PROMPT_COMMAND. Without the file the command was cut
# short by Ctrl+C, or returned: the request is over. agent-end goes before
# __aish_cur is cleared: a second SIGINT before it comes back here.
__aish_escaped() {
	local __aish_id __aish_code
	if [[ -s ${AISH_RUN-}/esc ]]; then
		IFS=' ' read -r __aish_id __aish_code <"$AISH_RUN/esc" || :
	fi
	if [[ ${__aish_id-} != "$__aish_cur" ]]; then
		__aish_cur=
		return 0
	fi
	printf '\e]6973;%s;agent-end;%s;%s;%s\a' "$__aish_nonce" "$__aish_id" "$__aish_code" "${PWD//[$'\a\e']/}"
	__aish_cur=
	: >|"$AISH_RUN/esc"
	"$AISH_BIN" agent resume "$__aish_id" "$__aish_code" || return 0
	__aish_run "${__aish_asked-}"
}

for __aish_km in emacs vi-insert; do
	bind -m "$__aish_km" -x '"\C-x\C-a": __aish_route'
	bind -m "$__aish_km" '"\C-x\C-b": accept-line'
	bind -m "$__aish_km" '"\C-m": "\C-x\C-a\C-x\C-b"'
	bind -m "$__aish_km" '"\C-j": "\C-x\C-a\C-x\C-b"'
done
unset __aish_km

# In vi command mode, after Esc, Enter is accept-line, which would run a
# request typed there as a command: it goes through the route there too.
# A key the user bound there to something else or bound sequences under
# stays his, and goes his way, past the route, as without aish.
# __aish_kms are the keymaps of Enter's macro. Once, at load: $(...) forks.
__aish_kms=(emacs vi-insert)
__aish_b=$'\n'$(bind -m vi-command -p; bind -m vi-command -s; bind -m vi-command -X)$'\n'
for __aish_k in '\C-m' '\C-j'; do
	[[ $__aish_b == *$'\n"'"$__aish_k"$'": accept-line\n'* && $__aish_b != *$'\n"'"$__aish_k"[!\"]* ]] || continue
	bind -m vi-command -x '"\C-x\C-a": __aish_route'
	bind -m vi-command '"\C-x\C-b": accept-line'
	bind -m vi-command "\"$__aish_k\": \"\\C-x\\C-a\\C-x\\C-b\""
	__aish_kms=(emacs vi-insert vi-command)
done
unset __aish_k __aish_b

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

# Tab completes, on top of what it did: the path after an @ that starts a
# word of a request, a line whose first word is no command (the file it
# attaches, agent/mentions.go; bash would complete host names there), a
# skill after a / that starts the line, and the subcommands of aish and
# their arguments, which aish lists itself (cmd/aish/completion.go). Bash
# asks complete -I for the first word and complete -D for a word of a
# command with no compspec of its own: those are ours. The user's, from
# ~/.bashrc (bash-completion's loader), become ours with his options, and
# ours calls his for any other word; one that is no function stays his,
# and so does a compspec of his for aish. Once, at load: $(...) forks.

# __aish_comp_D completes a word after the first. Of a command, as before;
# of a request, an @ word is a path, the rest is bash's own completion, not
# that of the user's -D: bash-completion would leave a compspec for the
# first word, one that knows no @, for the next Tab.
__aish_comp_D() {
	if type -t -- "${1-}" >/dev/null 2>&1; then
		if [[ -n ${__aish_comp_dprev-} ]]; then
			"$__aish_comp_dprev" "$@" && return 0
			return $?
		fi
	elif __aish_comp_mention "${2-}"; then
		return 0
	fi
	compopt -o bashdefault -o default 2>/dev/null || :
	return 0
}

# __aish_comp_I completes the first word: @path, a skill for /name if one
# fits, a subagent after & (__aish_comp_agent), else as before, by the
# user's -I or as command names.
__aish_comp_I() {
	__aish_comp_mention "${2-}" && return 0
	__aish_comp_skill "${2-}" && return 0
	__aish_comp_agent "${2-}" && return 0
	if [[ -n ${__aish_comp_iprev-} ]]; then
		"$__aish_comp_iprev" "$@" && return 0
		return $?
	fi
	compopt -o bashdefault -o default 2>/dev/null || :
	return 0
}

# __aish_comp_mention completes the word before the cursor if it is @path,
# @ at the start of it as agent/mentions.go reads one, and fails if not. $1
# is the part of it readline replaces: COMP_WORDBREAKS cuts words at @, =
# and :, and bash keeps the @ in the part after it. (An @ after another
# character, as in ?@path, bash takes for a host name's, compspecs
# unasked.) A path goes as typed, as the request does: no quoting, a
# directory with a / and no space after it. A name with a blank is no
# @path; dot files come for a dot typed.
__aish_comp_mention() {
	local __aish_w __aish_p __aish_k __aish_h __aish_f
	__aish_w=${COMP_LINE-}
	__aish_w=${__aish_w:0:${COMP_POINT-0}}
	__aish_w=${__aish_w##*[[:space:]]}
	[[ $__aish_w == @* && $__aish_w == *"$1" ]] || return 1
	__aish_p=${__aish_w#@}
	__aish_k=${__aish_w%"$1"} # what readline keeps of the word
	__aish_h=$__aish_p
	[[ $__aish_p == '~' || $__aish_p == '~/'* ]] && __aish_h=$HOME${__aish_p:1}
	COMPREPLY=()
	while IFS= read -r __aish_f; do
		[[ $__aish_f == *[[:space:]]* ]] && continue
		[[ ${__aish_f##*/} == .* && ${__aish_p##*/} != .* ]] && continue
		[[ -d $__aish_f ]] && __aish_f+=/
		if [[ $__aish_h != "$__aish_p" ]]; then
			[[ $__aish_f == "$HOME"/* ]] || continue
			__aish_f="~${__aish_f:${#HOME}}"
		fi
		__aish_f=@$__aish_f
		COMPREPLY+=("${__aish_f#"$__aish_k"}")
	done < <(compgen -f -- "$__aish_h")
	if ((${#COMPREPLY[@]} == 1)) && [[ ${COMPREPLY[0]} == */ ]]; then
		compopt -o nospace 2>/dev/null || :
	fi
	# Nothing else for the word: bash's default would add host names.
	compopt +o bashdefault +o default 2>/dev/null || :
	return 0
}

# __aish_comp_skill completes /name at the start of the line with the
# skills of __aish_is_skill's roots, and fails when none fits: the word is
# a path then. The roots are globbed in a subshell, nullglob and all, as
# the user's shopt may have it otherwise.
__aish_comp_skill() {
	local __aish_n __aish_w __aish_f
	__aish_n=${1#/}
	[[ $1 == /* && $__aish_n != *[!A-Za-z0-9_-]* ]] || return 1
	__aish_w=${COMP_LINE-}
	__aish_w=${__aish_w:0:${COMP_POINT-0}}
	[[ ${__aish_w%"$1"} != *[![:space:]]* ]] || return 1
	COMPREPLY=()
	while IFS= read -r __aish_f; do
		__aish_f=${__aish_f%/SKILL.md}
		__aish_f=${__aish_f##*/}
		[[ ${#__aish_f} -le 64 && $__aish_f != *[!A-Za-z0-9_-]* ]] || continue
		[[ " ${COMPREPLY[*]-} " == *" /$__aish_f "* ]] || COMPREPLY+=("/$__aish_f")
	done < <(
		set +f
		shopt -s nullglob
		shopt -u failglob nocaseglob dotglob
		GLOBIGNORE=
		__aish_w=$PWD
		set -- "$HOME/.claude" "${XDG_CONFIG_HOME:-$HOME/.config}/aish"
		while [[ -n $__aish_w ]]; do
			set -- "$@" "$__aish_w/.claude"
			__aish_w=${__aish_w%/*}
		done
		for __aish_w in "$@" /.claude; do
			for __aish_f in "$__aish_w/skills/$__aish_n"*/SKILL.md; do
				[[ -f $__aish_f ]] && printf '%s\n' "$__aish_f"
			done
		done
	)
	((${#COMPREPLY[@]} > 0))
}

# __aish_comp_agent completes NAME of a line "&NAME text" with the subagents
# of __aish_is_agent's roots, and fails when none fits. Bash ends a command
# at &: the word after it comes as the first word of a command, and nothing
# before it, the & included, is in COMP_LINE. It comes so after ; and | too,
# and a word typed at the start of a line comes the same. So the subagents
# come for an empty word, which -I gets only after such a separator, and
# for a word no command starts: one that does is the command's, as before.
# aish's own subagents come with those of the files.
__aish_comp_agent() {
	local __aish_n __aish_w __aish_f
	__aish_n=$1
	[[ $__aish_n != *[!A-Za-z0-9_-]* ]] || return 1
	COMPREPLY=()
	while IFS= read -r __aish_f; do
		__aish_agent_file "$__aish_f" || continue
		__aish_f=${__aish_f##*/}
		__aish_f=${__aish_f%.md}
		[[ ${#__aish_f} -le 64 && $__aish_f != *[!A-Za-z0-9_-]* ]] || continue
		[[ " ${COMPREPLY[*]-} " == *" $__aish_f "* ]] || COMPREPLY+=("$__aish_f")
	done < <(
		set +f
		shopt -s nullglob
		shopt -u failglob nocaseglob dotglob
		GLOBIGNORE=
		__aish_w=$PWD
		set -- "$HOME/.claude" "${XDG_CONFIG_HOME:-$HOME/.config}/aish"
		while [[ -n $__aish_w ]]; do
			set -- "$@" "$__aish_w/.claude"
			__aish_w=${__aish_w%/*}
		done
		for __aish_w in "$@" /.claude; do
			for __aish_f in "$__aish_w/agents/$__aish_n"*.md; do
				printf '%s\n' "$__aish_f"
			done
		done
	)
	for __aish_f in general-purpose Explore; do
		[[ $__aish_f != "$__aish_n"* || " ${COMPREPLY[*]-} " == *" $__aish_f "* ]] || COMPREPLY+=("$__aish_f")
	done
	((${#COMPREPLY[@]} > 0)) || return 1
	if [[ -n $__aish_n ]] && compgen -c -- "$__aish_n" >/dev/null; then
		COMPREPLY=()
		return 1
	fi
	return 0
}

# __aish_comp_aish completes the words of aish, which aish lists: the
# words before the cursor's and the start of that one go to it. They come
# quoted, as a session name with a blank in it has to.
__aish_comp_aish() {
	local __aish_c __aish_q
	COMPREPLY=()
	while IFS= read -r __aish_c; do
		printf -v __aish_q %q "$__aish_c"
		[[ $__aish_q == "${2-}"* ]] && COMPREPLY+=("$__aish_q")
	done < <("${AISH_BIN:-aish}" __complete "${COMP_WORDS[@]:0:COMP_CWORD}" "${2-}" </dev/null 2>/dev/null)
	return 0
}

complete -p aish >/dev/null 2>&1 || complete -F __aish_comp_aish aish
# Bash before 5.0 has no -I, 4.0 no -D: complete fails, and Tab stays.
for __aish_k in D I; do
	__aish_l=$(complete -p "-$__aish_k" 2>/dev/null) || __aish_l=
	__aish_f=
	if [[ -z $__aish_l ]]; then
		complete "-$__aish_k" -F "__aish_comp_$__aish_k" 2>/dev/null || :
	elif [[ $__aish_l == *' -F '* ]]; then
		__aish_f=${__aish_l#* -F }
		__aish_f=${__aish_f%% *}
		eval "${__aish_l/" -F $__aish_f "/" -F __aish_comp_$__aish_k "}" 2>/dev/null || __aish_f=
	fi
	if [[ $__aish_k == D ]]; then __aish_comp_dprev=$__aish_f; else __aish_comp_iprev=$__aish_f; fi
done
unset __aish_k __aish_l __aish_f

HISTIGNORE="${HISTIGNORE:+$HISTIGNORE:}__aish_ask *"
PS0='${__aish_ps0}'"${PS0-}"
# __aish_precmd returns the code of the user's command, for $? in PS1 and in
# the PROMPT_COMMAND after it. First in a list, that code, when not 0,
# neither runs the user's ERR trap a second time nor exits under set -e.
if [[ $(declare -p PROMPT_COMMAND 2>/dev/null) == "declare -a"* ]]; then
	PROMPT_COMMAND=('__aish_precmd && :' "${PROMPT_COMMAND[@]}")
else
	PROMPT_COMMAND="__aish_precmd && :${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
fi
