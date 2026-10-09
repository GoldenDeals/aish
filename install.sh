#!/bin/sh
# Builds aish from this checkout and installs it for the current user: the
# binary, Tab completion of aish for bash and zsh, and the examples, put
# where they do nothing; then prints how to run aish in place of the shell.
# It needs no sudo and touches no rc file and no config of the user. Run it
# again after git pull to update: the binary and the completion files are
# replaced when they changed, an example already there is never overwritten.
#
#   PREFIX  the binary goes to $PREFIX/bin (default ~/.local)
#   BINDIR  the binary's directory itself (default $PREFIX/bin)
#
# The completion files go where bash-completion and zsh look for a user's
# own, under $XDG_DATA_HOME (~/.local/share); the examples beside the config
# aish reads, in $XDG_CONFIG_HOME/aish (~/.config/aish).
set -eu

: "${HOME:?is not set}"
PREFIX=${PREFIX:-$HOME/.local}
BINDIR=${BINDIR:-$PREFIX/bin}
data=${XDG_DATA_HOME:-$HOME/.local/share}
# bash-completion takes a list here; a user's file goes to the first.
bashcomp=${BASH_COMPLETION_USER_DIR:-$data/bash-completion}
bashcomp=${bashcomp%%:*}/completions
zshcomp=$data/zsh/site-functions
conf=${XDG_CONFIG_HOME:-$HOME/.config}/aish
examples=$conf/examples

# A relative BINDIR is of the directory install.sh starts in, not of the
# checkout it goes to.
case $BINDIR in
/*) ;;
*) BINDIR=$PWD/$BINDIR ;;
esac
cd -- "$(dirname -- "$0")"
if [ ! -f cmd/aish/main.go ]; then
	echo "install.sh: cmd/aish not found; run the install.sh of an aish checkout" >&2
	exit 1
fi
if ! command -v go >/dev/null 2>&1; then
	echo "install.sh: go not found; aish builds with Go of the version in go.mod (https://go.dev/dl/)" >&2
	exit 1
fi

tmp=$(mktemp -d)
partial=
cleanup() {
	rm -rf -- "$tmp"
	if [ -n "$partial" ]; then
		rm -f -- "$partial"
	fi
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

# tilde shows a path under $HOME as ~/…: shorter, and a command with it
# still works if $HOME has a space.
tilde() {
	# shellcheck disable=SC2088 # the ~ is printed
	case $1 in
	"$HOME"/*) printf '~/%s' "${1#"$HOME"/}" ;;
	*) printf '%s' "$1" ;;
	esac
}

note() {
	printf '  %-10s %s\n' "$1" "$(tilde "$2")"
}

# put SRC DEST MODE makes DEST a copy of SRC through a file beside it and a
# rename: an aish running from DEST keeps its binary, and DEST stays as it
# was if the copy fails. A DEST equal to SRC is left alone.
binary_changed=
put() {
	if [ -f "$2" ] && cmp -s -- "$1" "$2"; then
		note unchanged "$2"
		return
	fi
	mkdir -p -- "$(dirname -- "$2")"
	partial=$(dirname -- "$2")/.$(basename -- "$2").new
	cp -- "$1" "$partial"
	chmod "$3" "$partial"
	mv -f -- "$partial" "$2"
	partial=
	note installed "$2"
	if [ "$2" = "$BINDIR/aish" ]; then
		binary_changed=1
	fi
}

echo "Building aish..."
go build -trimpath -o "$tmp/aish" ./cmd/aish
"$tmp/aish" completion bash >"$tmp/aish.bash"
"$tmp/aish" completion zsh >"$tmp/_aish"

echo "Installing:"
put "$tmp/aish" "$BINDIR/aish" 755
put "$tmp/aish.bash" "$bashcomp/aish" 644
put "$tmp/_aish" "$zshcomp/_aish" 644

# The examples go where aish reads nothing: a policy, a hook or a tool in
# its own directory works at once, and the user picks what to turn on. One
# already there stays as it is: the user may have edited it.
list=$(cd examples && find . -type f | sort)
set -f
saved_ifs=$IFS
IFS='
'
for f in $list; do
	f=${f#./}
	if [ -e "$examples/$f" ]; then
		note kept "$examples/$f"
		continue
	fi
	mkdir -p -- "$(dirname -- "$examples/$f")"
	cp -- "examples/$f" "$examples/$f"
	note installed "$examples/$f"
done
IFS=$saved_ifs
set +f

bin=$(tilde "$BINDIR")
c=$(tilde "$conf")
e=$(tilde "$examples")
z=$(tilde "$zshcomp")

echo
case ":${PATH-}:" in
*":$BINDIR:"*)
	found=$(command -v aish 2>/dev/null || :)
	# Inside aish, $AISH_RUN/bin has an aish of its own when PATH had none.
	case $found in
	"" | "$BINDIR/aish" | "${AISH_RUN:-/nonexistent}/bin/aish") ;;
	*)
		echo "Note: another aish comes first in PATH: $(tilde "$found")"
		echo
		;;
	esac
	;;
*)
	# Not ~ in the line: it stays a ~ in double quotes.
	case $BINDIR in
	"$HOME"/*) dir="\$HOME/${BINDIR#"$HOME"/}" ;;
	*) dir=$BINDIR ;;
	esac
	echo "Note: $bin is not in PATH; add it in ~/.profile or ~/.bashrc:"
	echo "  export PATH=\"$dir:\$PATH\""
	echo
	;;
esac
if [ -n "${AISH_SOCK-}" ] && [ -n "$binary_changed" ]; then
	echo "Note: this shell runs under aish, which keeps the old binary till you start aish anew."
	echo
fi

cat <<EOF
Tab completion of aish:
  bash  bash-completion loads the file by itself; without bash-completion, add to ~/.bashrc:
          source <(aish completion bash)
  zsh   add to ~/.zshrc, before compinit:
          fpath=($z \$fpath)
  Under aish, Tab completes its commands without these.

The examples in $e do nothing there. To use one, copy it to
the directory of its kind; each file tells at its top what it does:
  $e/policy/*.cedar   ->  $c/policy/
  $e/hooks/EVENT/*    ->  $c/hooks/EVENT/
  $e/tools/*          ->  $c/tools/
e.g.
  mkdir -p $c/tools && cp $e/tools/weather $c/tools/
Hooks and tools work from the next request, a policy from the next aish
or, in a running one, after aish apply-config.

Start aish with a key of the model's API in the environment
(AISH_API_KEY, ANTHROPIC_API_KEY or OPENAI_API_KEY):
  aish

To run aish in place of your shell, either make aish the command of your
terminal emulator (in its profile settings), or add this line at the end of
~/.bashrc (~/.zshrc for zsh):
EOF
# shellcheck disable=SC2016 # the line is printed, not run
printf '  %s\n' '[[ -z ${AISH_SOCK-} && ${AISH_FALLBACK-} != $$ && $- == *i* ]] && command -v aish >/dev/null && exec aish'
cat <<'EOF'
The shell aish starts reads that file too and skips the line: AISH_SOCK is
set there. $- == *i* keeps aish out of scripts; command -v keeps exec from
closing the terminal if aish is gone. aish starts bash; for zsh, see
"zsh" in README.md. Should aish fail to start (a broken config, say), it
prints why and starts your shell in its place, rc files and all, with its
pid in AISH_FALLBACK, for the line to skip it: fix what is wrong there and
run aish. Do not make aish your login shell (chsh): ssh with a command and
scp run that with -c, which aish does not take.
EOF
