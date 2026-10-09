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
typeset -g __aish_route_expand=false
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
typeset -g __aish_spawn=   # NAME of a line "&NAME text", for __aish_ask
typeset -g __aish_req=     # the text __aish_ask "$__aish_req" sends
typeset -g __aish_typed=   # what was typed, for history
typeset -g __aish_redraw=  # 1: __aish_ask replaces the line on the screen
typeset -g __aish_intr=    # Ctrl+C cut the expansion short: its code, till the prompt
typeset -g __aish_hint= __aish_hinted= __aish_rc=0 __aish_based= __aish_autocd=off

# Every function but those that run the user's code (__aish_ask, the
# expansion of a request) and those that must leave what they do to the
# shell (__aish_precmd sourcing restore.bash) starts with emulate -L zsh:
# the user's options stay out of it.

# __aish_accept is accept-line: the line is routed first. A request whose
# line ends in an odd run of backslashes goes on to the next one
# (__aish_more): zle keeps the line, and nothing of it is expanded yet.
__aish_accept() {
	typeset -g __aish_autocd=${options[autocd]}
	__aish_route
	if [[ -n $__aish_t || -n $__aish_raw || -n $__aish_spawn ]] && __aish_more; then
		typeset -g __aish_t= __aish_raw=
		return 0
	fi
	if [[ -n $__aish_t && -z $__aish_raw && $__aish_route_expand == true && $__aish_t == *'$'* ]]; then
		__aish_expanding
	fi
	if [[ -n $__aish_t || -n $__aish_raw || -n $__aish_spawn ]]; then
		__aish_request "${options[errexit]}"
	fi
	typeset -g __aish_t= __aish_raw=
	zle __aish_accept_prev -- "$@"
}

# __aish_route decides whether BUFFER is the shell's or a request: for one it
# sets __aish_t to its text, else __aish_ps0 for preexec.
__aish_route() {
	emulate -L zsh -o extendedglob
	local __aish_line __aish_trim __aish_w __aish_end
	__aish_t= __aish_raw= __aish_spawn=
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
	('&'[A-Za-z0-9_-]*)
		# "&reviewer check the diff": the subagent, in the background, as in
		# init.bash. To zsh & and a word are a parse error; &>file is a
		# redirection, and stays its.
		__aish_spawn=${${__aish_trim%%[[:space:]]*}#\&}
		__aish_t=${__aish_trim#\&$__aish_spawn}
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

# __aish_more puts a newline for the last backslash of the line and the
# blanks before it, as init.bash's does, when the line ends in an odd run
# of them.
__aish_more() {
	emulate -L zsh -o extendedglob
	local e
	e=${(M)BUFFER%%\\#}
	(($#e % 2)) || return 1
	e=${BUFFER%\\}
	BUFFER=${e%%[[:blank:]]#}$'\n'
	CURSOR=$#BUFFER
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

# __aish_is_agent: is there a subagent named $1 here? The roots are those of
# subagent.Find (internal/subagent), aish's own subagents the farthest, as in
# init.bash.
__aish_is_agent() {
	emulate -L zsh
	local n d MATCH MBEGIN MEND match mbegin mend
	n=$1
	[[ $n =~ '^[A-Za-z0-9_-]{1,64}$' ]] || return 1
	for d in "$HOME/.claude" "${XDG_CONFIG_HOME:-$HOME/.config}/aish"; do
		__aish_agent_file "$d/agents/$n.md" && return 0
	done
	d=$PWD
	while [[ -n $d ]]; do
		__aish_agent_file "$d/.claude/agents/$n.md" && return 0
		d=${d%/*}
	done
	__aish_agent_file "/.claude/agents/$n.md" && return 0
	[[ $n == (general-purpose|Explore) ]]
}

# __aish_agent_file: does the file $1 start with a frontmatter, as
# subagent.Find takes a subagent's to? A README beside them does not.
__aish_agent_file() {
	emulate -L zsh -o extendedglob
	local l
	[[ -f $1 && -r $1 ]] || return 1
	IFS= read -r l <$1 || [[ -n $l ]] || return 1
	l=${l#$'\xef\xbb\xbf'}
	[[ ${${l##[[:space:]]#}%%[[:space:]]#} == --- ]]
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

# __aish_split sets p, an array of __aish_body, to $1 cut before every ',
# $(, ${, $[ and $\<newline>: __aish_body walks the pieces, not the
# characters. The cut is a byte the text lacks, left in d, a local of
# __aish_body; with none left it fails, and the text stays as typed. Bytes:
# a pattern costs more on a multibyte string.
__aish_split() {
	emulate -L zsh +o multibyte
	local c s
	for d in $'\x1f' $'\x1e' $'\x1d' $'\x1c' ''; do
		[[ $1 != *$d* ]] && break
	done
	[[ -n $d ]] || return 1
	c=$1
	for s in \' '$(' '${' '$[' $'$\\\n'; do
		c=${c//$s/$d$s}
	done
	p=("${(@ps:$d:)c}")
}

# __aish_body sets __aish_b, a local of __aish_expand, to $1 made ready for
# ${(e)...}, which reads it as zsh reads a string in double quotes. In a
# single quote of the top level a backslash goes before every \, $ and `;
# elsewhere a backtick is escaped and the backslashes before it doubled, and
# at the top level the other backslashes are doubled too, but for the last
# of an odd run before a $: the text keeps them as typed, a
# backslash-newline joins no lines, and \$ is a dollar. A run before a $
# keeps its parity, so what is expanded stays the same. In $(...) and ${...}
# backslashes are shell syntax and stay. This is init.bash's __aish_body,
# with zsh's reading for bash's.
#
# The quote is kept only where zsh, reading the text as typed, is at the
# top level at both its ends, so nothing runs that did not: the walk follows
# zsh through $(...), ${...} and the quotes in them, a stack of what closes
# each (^ is the top level). What it cannot follow for sure, or where bash
# reads the text otherwise, ends it: a comment, case, a here-document or
# (( in $(...), a line continuation there or after $, $'...', $[...],
# $((...)) (whose quotes are text to zsh), $$ before a quote or a
# substitution; in ${...} \", a {, and a quote but one that hides nothing
# from either; and $( itself when an alias may leave something open. Then
# the text is expanded as before: as typed when it has a backslash or a
# single quote that starts a word, else all of it as in double quotes. So
# it is, too, when the walk ends inside something, which zsh would fail to
# read.
__aish_body() {
	emulate -L zsh -o extendedglob
	local -a p g
	local -A v # for a piece that ends at the top level, its rest from where the top level starts
	local d c a h s k y n u
	integer i j o z
	k=^
	if [[ $1 == *[\'\\]* ]]; then
		__aish_split "$1" || return 1
		# An alias is code zsh reads in $(...) too, a global one anywhere:
		# one that leaves a quote, a parenthesis or case open, or starts a
		# comment or a here-document, would have the walk follow other code
		# than zsh does.
		if [[ $1 == *\$\(* ]]; then
			for s in "${(@v)aliases}" "${(@v)galiases}" "${(@v)saliases}"; do
				[[ $s == *(\\|case|esac|\<\<|\#)* ]] && u=1
				a=${s//[^\']} c=${s//[^\"]} h=${s//[^\`]}
				(($#a % 2 || $#c % 2 || $#h % 2)) && u=1
				a=${s//[^\(]} c=${s//[^\)]}
				(($#a != $#c)) && u=1
			done
		fi
	else
		p=("$1")
	fi
	v[1]=$p[1]
	for ((i = 2; i <= $#p; i++)); do
		c=$p[i]
		# y is the piece with the open quote, if any.
		if [[ -n $y && $c == \'* && $c[2] != [[:alnum:]] ]]; then
			if [[ $k == '^' ]]; then
				for ((j = y; j < i; j++)); do
					s=${p[j]//\\/\\\\}
					s=${s//\$/\\\$}
					p[j]=${s//\`/\\\`}
				done
				g+=($y $i)
				y= v[$i]=$c
				continue
			fi
			y=
		fi
		o=2 # where the rest of the piece to walk starts
		case $k[-1]$c[1] in
		(\'\') k=${k%?} ;;
		(\'?) continue ;;
		(\^\')
			[[ -z $y && ${p[i-1][-1]} != [[:alnum:]\\] ]] && y=$i
			v[$i]=$c
			continue
			;;
		(*)
			[[ $c == \$[\[\\]* ]] && break
			# An odd run of backslashes before it makes its first
			# character text.
			a=${(M)p[i-1]%%\\#}
			if (($#a % 2 == 0)); then
				[[ $p[i-1] == *\$ && $k[-1]$c[1] != \"\' ]] && break
				case $k[-1]$c[1,3] in
				(?\$\(\() break ;;
				(\)\'*)
					k+=\'
					continue
					;;
				(\}\'*)
					# In ${...} of a command a quote is one; of a string in
					# double quotes, text, which bash reads as a quote: only
					# one with no } " \ { or substitution in it is the same
					# to both, and z is the piece that closes it.
					n=${k%%\}##}
					if [[ $n[-1] == \) ]]; then
						k+=\'
						continue
					fi
					if ((z != i)); then
						[[ $c[2,-1] != *[\}\"\\\{]* && $p[i+1] == \'* ]] || break
						z=$((i + 1))
					fi
					;;
				(?\$\(*)
					[[ -n $u ]] && break
					k+=\) o=3
					;;
				(?\$\{*) k+=\} o=3 ;;
				esac
			fi
			;;
		esac
		if [[ $k == *[\^\'] ]]; then
			[[ $k == '^' ]] && v[$i]=$c
			continue
		fi
		h=$c[o,-1]
		while :; do
			case $k[-1] in
			(\))
				s=${h%%[\\\"\(\)]*}
				[[ $s == *[\#]* || $s == *\<\<* || $s == *case* ]] && break 2
				;;
			(\}) s=${h%%[\\\"\{\}]*} ;;
			(\") s=${h%%[\\\"]*} ;;
			(*) break ;;
			esac
			[[ $s == "$h" ]] && break
			h=$h[$#s+1,-1]
			case $k[-1]$h[1,2] in
			(\)\\$'\n' | \}\\\") break 2 ;;
			(?\\*) h=$h[2,-1] ;;
			(\)\(\(*) break 2 ;;
			(\)\"* | \}\"*) k+=\" ;;
			(\)\(*) k+=\) ;;
			(\)\)* | \}\}* | \"\"*) k=${k%?} ;;
			(\}\{*) break 2 ;;
			esac
			h=$h[2,-1]
		done
		[[ $k == '^' ]] && v[$i]=$h
	done
	if ((i <= $#p)) || [[ $k != '^' ]]; then
		[[ $1 == *\\* || $1 == \'* || $1 == *[^[:alnum:]]\'* ]] && return 1
		__aish_b=${1//\`/\\\`}
		return 0
	fi
	# Backticks and backslashes out of the kept quotes: g holds where each
	# starts and where its closing piece is. Bytes, as in __aish_split.
	if [[ $1 == *[\`\\]* ]]; then
		setopt nomultibyte
		g+=($(($#p + 1)) 0)
		i=1
		for ((j = 1; j <= $#g; j += 2)); do
			for (( ; i < g[j]; i++)); do
				h=$p[i] a= c=
				[[ $h == *[\`\\]* ]] || continue
				if ((${+v[$i]})); then
					# The top level of the piece: the backslashes doubled, but
					# for the last of an odd run before a $, the pairs first,
					# as zsh reads them, to d. A piece ends where the next
					# starts with ' or $: the $ is put back for the
					# backslashes before it.
					c=$v[$i] n=
					h=$h[1,$#h-$#c]
					[[ $p[i+1] == \$* ]] && n=\$
					c+=$n
					if [[ $c == *\\* ]]; then
						c=${c//\\\\/$d}
						c=${c//\\/\\\\}
						c=${c//\\\\\$/\\\$}
						c=${c//$d/\\\\\\\\}
					fi
					c=${c//\`/\\\`}
					[[ -n $n ]] && c=${c%\$}
				fi
				while [[ $h == *\`* ]]; do
					s=${h%%\`*}
					# Doubled, a backslash before the backtick cannot take
					# the one that escapes it.
					a+=$s${(M)s%%\\#}\\\`
					h=${h#*\`}
				done
				p[i]=$a$h$c
			done
			i=$g[j+1]
		done
	fi
	__aish_b=${(j::)p}
}

# __aish_expanding sets __aish_t to its text expanded: $VAR, ${...}, $(...)
# and $((...)) as in double quotes, but in single quotes, so that the
# screen, the journal and the model get the same text, and history what was
# typed (__aish_body). It runs with the user's options, as the text would.
# A $(...) may take long: "expanding…" stands below the line meanwhile, and
# Ctrl+C ends only the substitution. Untrapped, it would end the widget and
# throw the line away. __aish_intr keeps the code, 130 or that of the signal
# that killed the substitution, for __aish_ask to return unasked; the
# user's trap is back when this returns (localtraps). It returns 0 under
# the user's err_return too, and a substitution that fails is in a list:
# it neither returns under err_return nor exits under err_exit.
__aish_expanding() {
	setopt localoptions localtraps
	local __aish_x __aish_s
	if [[ $__aish_t == *'$('* ]]; then
		trap 'typeset -g __aish_intr=130' INT
		zle -R 'expanding…'
	fi
	__aish_x=$(__aish_expand </dev/null 2>/dev/null) && :
	__aish_s=$?
	[[ $__aish_t == *'$('* ]] && zle -R ''
	((__aish_s > 128)) && typeset -g __aish_intr=$__aish_s
	[[ -z $__aish_intr ]] && ((__aish_s == 0)) && [[ $__aish_x == *. ]] && typeset -g __aish_t=${__aish_x%.}
	return 0
}

# __aish_expand prints __aish_t expanded and a dot after it: the command
# substitution takes the newlines at the end, not the dot. $? in the text is
# the code of the user's last command; $1 is no word of it. It fails when
# the text stays as typed. In a list, a code other than 0 neither returns
# under err_return nor exits under err_exit.
__aish_expand() {
	local __aish_b
	__aish_body "$__aish_t" || return 1
	set --
	__aish_status "$__aish_rc" && :
	print -rn -- "${(e)__aish_b}."
}

__aish_status() { return $1; }

# __aish_request rewrites the line into a request; $1 is the user's
# err_exit, on or off.
__aish_request() {
	emulate -L zsh -o extendedglob
	__aish_t=${${__aish_t##[[:space:]]#}%%[[:space:]]#}
	# &NAME without text goes on too: __aish_spawning tells what it lacks.
	if [[ -z $__aish_t && -z $__aish_spawn ]]; then
		BUFFER=
	else
		__aish_req=$__aish_t
		BUFFER='__aish_ask "$__aish_req"'
		# Under the user's err_exit a request that fails, 130 after Ctrl+C
		# among them, would close the shell. In a list it does not, and $?
		# is still the request's code. Only under err_exit: the list keeps
		# the user's ZERR trap from it too, as init.bash does under set -e.
		[[ $1 == on ]] && BUFFER+=' && :'
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
# there instead. The code 1 that says so neither exits under the user's
# err_exit nor runs his ZERR trap, which emulate -L puts back on return.
__aish_addhistory() {
	emulate -L zsh
	[[ $1 == '__aish_ask "$__aish_req"'* ]] || return 0
	trap - ZERR
	return 1
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
	[[ -z ${__aish_comp_wait-} ]] || __aish_comp_init
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
			# In a list, a command of it that fails neither returns under
			# the user's err_return, before cmd-end, nor exits under err_exit.
			builtin source "$AISH_RUN/restore.bash" && :
			: >|"$AISH_RUN/restore.bash"
		fi
		# Written before cmd-end: the proxy reads it when the marker arrives.
		__aish_dump >|"$AISH_RUN/state"
	fi
	printf '\e]6973;%s;cmd-end;%s;%s\a' "$__aish_nonce" "$__aish_rc" "${PWD//[$'\a\e']/}"
	typeset -g __aish_ps0= __aish_intr=
	return 0
}

# __aish_unecho replaces the `__aish_ask "$__aish_req"` line zle has left on
# the screen with the request, the prompt's % (or #, $, ❯, >) turned into
# ?: "host? text". The prompt is expanded with the user's options, %? the
# code of the user's last command: in a list, a code other than 0 neither
# returns under err_return nor exits under err_exit.
__aish_unecho() {
	local __aish_p
	__aish_status "$__aish_rc" && :
	__aish_p=${(%%)PS1}
	__aish_unecho_draw "$1" "$__aish_p" "${options[errexit]}"
}

# __aish_unecho_draw TEXT PROMPT [ERREXIT]: with ERREXIT on the line zle
# left ends in ` && :`, as __aish_request writes it under err_exit.
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
	[[ ${3-} == on ]] && line+=' && :'
	w=$((${(m)#vis} + ${#line}))
	# zle takes the cursor to the next row after a full one, and Enter one
	# row down from there: a line just the terminal's width takes two.
	rows=$((w / cols + 1))
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
		__aish_unecho "${__aish_spawn:+&$__aish_spawn${1:+ }}$1"
	fi
	local __aish_q __aish_id __aish_cmd __aish_rc __aish_e __aish_c __aish_a
	__aish_q=$1 __aish_a=$__aish_spawn
	# $1 keeps the text for the agent's commands; the global would keep it
	# after the request.
	typeset -g __aish_req= __aish_spawn=
	print -rs -- "${__aish_typed:-$__aish_q}"
	typeset -g __aish_typed=
	# Ctrl+C cut its expansion short: on the screen and in history, unsent.
	if [[ -n $__aish_intr ]]; then
		__aish_rc=$__aish_intr
		typeset -g __aish_intr=
		return $__aish_rc
	fi
	if [[ -n $__aish_a ]]; then
		# In a list its code 2, a hint, runs the user's ZERR trap once, as
		# the request's code, not in __aish_spawning too.
		__aish_spawning "$__aish_a" "$__aish_q" && :
		return
	fi

	printf '\e]6973;%s;ask-start\a' "$__aish_nonce"
	"$AISH_BIN" agent start -- "$__aish_q" || return
	while [[ -s $AISH_RUN/next.cmd ]]; do
		IFS= read -r __aish_id <"$AISH_RUN/next.id" || :
		IFS= read -r -d '' __aish_cmd <"$AISH_RUN/next.cmd" || :
		: >|"$AISH_RUN/next.cmd"
		__aish_rc=${__aish_cmd//[$'\a\e']/}
		# Esc stops the command with SIGINT, once the proxy has written
		# "<call id> <code>" to $AISH_RUN/esc: the always block finds the
		# interrupt, resets it, and the command ends with that code. An
		# interrupt breaks every loop it is in, this one too: break in the
		# always block takes that for the loop of one round around it.
		# Without the file the interrupt is Ctrl+C's and ends the request.
		# agent-start is in the try block: the proxy may signal on it.
		repeat 1; do
			{
				printf '\e]6973;%s;agent-start;%s;%s\a' "$__aish_nonce" "$__aish_id" "${__aish_rc:0:1000}"
				# In a condition, a command of it that fails neither returns
				# under the user's err_return before agent-end nor exits under
				# err_exit. A list would not do: under err_exit the request is
				# the left of one (`&& :`), and in a function called there zsh
				# leaves err_return on in the left of a list.
				if eval "$__aish_cmd" </dev/null; then
					__aish_rc=0
				else
					__aish_rc=$?
				fi
			} always {
				if [[ -s $AISH_RUN/esc ]]; then
					IFS=' ' read -r __aish_e __aish_c <"$AISH_RUN/esc" || :
					if [[ $__aish_e == "$__aish_id" ]]; then
						: >|"$AISH_RUN/esc"
						# Not interrupted when a trap of the user's took
						# SIGINT: the command ended as the trap left it.
						if ((TRY_BLOCK_INTERRUPT)); then
							__aish_rc=$__aish_c
							TRY_BLOCK_INTERRUPT=0
							break
						fi
					fi
				fi
			}
		done
		printf '\e]6973;%s;agent-end;%s;%s;%s\a' "$__aish_nonce" "$__aish_id" "$__aish_rc" "${PWD//[$'\a\e']/}"
		"$AISH_BIN" agent resume "$__aish_id" "$__aish_rc" || break
	done
}

# __aish_spawning starts subagent $1 in the background on $2, the text of a
# line "&NAME text", as in init.bash; a name no agents directory here has a
# file of, or no text, gets a hint.
__aish_spawning() {
	emulate -L zsh
	if ! __aish_is_agent "$1"; then
		print -ru2 -- "aish: no subagent $1 here; aish agents lists them"
		return 2
	fi
	if [[ -z $2 ]]; then
		print -ru2 -- "aish: what is $1 to do? &$1 TEXT"
		return 2
	fi
	"$AISH_BIN" agent spawn "$1" -- "$2"
}

# Ctrl+V (or Ctrl+Q) before a paste, a habit where the terminal pastes with
# Ctrl+Shift+V: quoted-insert would take the paste's ESC for the character
# and the rest as typed: the line would be ^[[200~text~, and a newline in
# the text would run it. __aish_quote is quoted-insert (vi-quoted-insert for
# __aish_vi_quote) that takes a bracketed paste after it as a paste: it
# waits for the key as long as it takes, the paste may come a second after
# Ctrl+V, and the rest of a paste's sequence comes at once after its ESC.
# Tab, Esc or another key is the character still, and what came after an
# ESC goes on as typed.
__aish_quote() {
	emulate -L zsh
	local k s w
	w=quoted-insert
	[[ $WIDGET == __aish_vi_quote ]] && w=vi-quoted-insert
	read -k 1 k || return 1
	if [[ $k == $'\e' ]]; then
		s=$k
		while [[ $s != $'\e[200~' && $'\e[200~' == "$s"* ]] && read -k 1 -t 0.1 k; do
			s+=$k
		done
		if [[ $s == $'\e[200~' ]]; then
			zle bracketed-paste
			return
		fi
		zle -U -- "${s[2,-1]}"
		k=$'\e'
	fi
	# quoted-insert reads the key from what -U pushed.
	zle -U -- "$k"
	zle .$w
}
zle -N __aish_quote
zle -N __aish_vi_quote __aish_quote

# The keys go to it in emacs and viins where they are quoted-insert's or
# vi-quoted-insert's, zsh's own, and the paste's sequence is bracketed-paste:
# a key the user bound to something else, bound sequences under, or whose
# widget he redefined stays his. Once, at load: $(...) forks.
() {
	local km k w
	for km in emacs viins; do
		w=$(bindkey -M $km '^[[200~')
		[[ ${w##* } == bracketed-paste ]] || continue
		for k in '^V' '^Q'; do
			w=$(bindkey -M $km $k)
			w=${w##* }
			[[ $w == (vi-|)quoted-insert && $widgets[$w] == builtin ]] || continue
			[[ -z $(bindkey -M $km -p $k) ]] || continue
			if [[ $w == vi-* ]]; then
				bindkey -M $km $k __aish_vi_quote
			else
				bindkey -M $km $k __aish_quote
			fi
		done
	done
}

# Tab, with the completion system the user's .zshrc loaded (compinit),
# completes on top of what it did: the path after an @ that starts a word
# of a request, a line whose first word is no command, a skill after a /
# that starts the line, and the subcommands of aish and their arguments,
# which aish lists itself. Compsys runs -first- before any other
# completion: ours runs the user's for any other word. A completion of his
# for aish stays. After a compinit run later, Tab is so from the next
# prompt; without compinit Tab stays as it was.

# __aish_comp_first is -first-. A word it completes ends the completion
# there, which _compskip, a local of compsys's, tells it.
__aish_comp_first() {
	if __aish_comp_ours; then
		_compskip=all
		return 0
	fi
	[[ -n $__aish_comp_prev ]] && eval "$__aish_comp_prev"
}

# __aish_comp_ours completes the word under the cursor if it is aish's: an
# @path after a first word that is no command, or first, @path or ?@path,
# NAME after an & that starts the line, and /name if a skill fits. Zsh ends
# a command at &, and the word after it is the first one: the line before
# it tells.
__aish_comp_ours() {
	emulate -L zsh -o extendedglob
	[[ -z $compstate[quote] && -z $IPREFIX ]] || return 1
	if ((CURRENT == 1)); then
		if [[ $PREFIX == (\?|)@* ]]; then
			__aish_comp_mention
			return 0
		fi
		if [[ $PREFIX == [A-Za-z0-9_-]# && $LBUFFER == [[:space:]]#\&$PREFIX ]]; then
			__aish_comp_agent
			return 0
		fi
		[[ $PREFIX == /[A-Za-z0-9_-]# ]] && __aish_comp_skill
		return
	fi
	[[ $PREFIX == @* ]] || return 1
	whence -- "$words[1]" >/dev/null 2>&1 && return 1
	__aish_comp_mention
	return 0
}

# __aish_comp_mention completes the path after the @ as agent/mentions.go
# reads it, the request going as typed: no quoting, a directory with a /
# and nothing after it. A name with a blank is no @path; dot files come for
# a dot typed.
__aish_comp_mention() {
	emulate -L zsh -o extendedglob
	local d f n
	local -a found dirs files
	compset -P '(\?|)@'
	d=${(M)PREFIX##*/}
	compset -P '*/'
	[[ $d == \~(/*|) ]] && d=$HOME${d#\~}
	if [[ $PREFIX == .* ]]; then
		found=($d*(DN))
	else
		found=($d*(N))
	fi
	for f in $found; do
		n=${f##*/}
		[[ $n == *[[:space:]]* ]] && continue
		if [[ -d $f ]]; then
			dirs+=("$n/")
		else
			files+=("$n")
		fi
	done
	compadd -Q -S '' -a dirs
	compadd -Q -a files
}

# __aish_comp_skill completes /name with the skills of __aish_is_skill's
# roots, and fails, leaving the word as it was, when none fits: the word is
# a path then.
__aish_comp_skill() {
	emulate -L zsh -o extendedglob
	local d f p ip
	local -a roots names
	roots=("$HOME/.claude" "${XDG_CONFIG_HOME:-$HOME/.config}/aish")
	d=$PWD
	while [[ -n $d ]]; do
		roots+=("$d/.claude")
		d=${d%/*}
	done
	for d in $roots /.claude; do
		for f in $d/skills/*/SKILL.md(N-.); do
			f=${f:h:t}
			[[ $f == [A-Za-z0-9_-](#c1,64) ]] && names+=("$f")
		done
	done
	p=$PREFIX ip=$IPREFIX
	compset -P '/'
	compadd -Q -- ${(u)names} && return 0
	PREFIX=$p IPREFIX=$ip
	return 1
}

# __aish_comp_agent completes NAME of a line "&NAME text" with the subagents
# of __aish_is_agent's roots, aish's own too.
__aish_comp_agent() {
	emulate -L zsh -o extendedglob
	local d f
	local -a roots names
	roots=("$HOME/.claude" "${XDG_CONFIG_HOME:-$HOME/.config}/aish")
	d=$PWD
	while [[ -n $d ]]; do
		roots+=("$d/.claude")
		d=${d%/*}
	done
	for d in $roots /.claude; do
		for f in $d/agents/*.md(N-.); do
			__aish_agent_file $f || continue
			f=${f:t:r}
			[[ $f == [A-Za-z0-9_-](#c1,64) ]] && names+=("$f")
		done
	done
	names+=(general-purpose Explore)
	compadd -Q -- ${(u)names}
}

# __aish_comp_aish completes the words of aish, which aish lists: the words
# before the cursor's and the start of that one go to it.
__aish_comp_aish() {
	emulate -L zsh
	local -a v
	v=("${(@f)$("${AISH_BIN:-aish}" __complete "${(@)words[1,CURRENT-1]}" "$PREFIX" </dev/null 2>/dev/null)}")
	compadd -- "${(@)v:#}"
}

# __aish_comp_init sets Tab up if compinit has run, and else sets
# __aish_comp_wait: a compinit later than the .zshrc (zinit's turbo,
# zsh4humans) __aish_precmd looks for at each prompt till it comes, with
# no command run. A compdef without _comps is no compinit's: zsh4humans's
# keeps the calls for after its compinit, and the -first- and aish they
# would replace are not known before it.
__aish_comp_init() {
	emulate -L zsh
	if (( ! ${+functions[compdef]} || ! ${+_comps} )); then
		typeset -g __aish_comp_wait=1
		return 0
	fi
	typeset -g __aish_comp_wait= __aish_comp_prev=${_comps[-first-]-}
	compdef __aish_comp_first -first-
	(( ${+_comps[aish]} )) || compdef __aish_comp_aish aish
	return 0
}

__aish_comp_init

zle -A accept-line __aish_accept_prev
zle -N accept-line __aish_accept
preexec_functions+=(__aish_preexec)
precmd_functions+=(__aish_precmd)
zshaddhistory_functions+=(__aish_addhistory)

options+=("${(@kv)__aish_opts[@]}")
\unset __aish_opts
