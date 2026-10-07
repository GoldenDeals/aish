package policy

import (
	"context"
	"slices"
	"testing"
)

// The commands of git that run code of their options and words: the code
// is among the commands of the line; a word made at run time that may be
// such an option or such code marks the line computed, and so does the word
// xargs appends to them.
func TestGitSubcommandCode(t *testing.T) {
	sudoLs := []string{"sudo", "ls"}
	for _, c := range []struct {
		src     string
		here    [][]string
		dynamic []string
	}{
		{`git rebase -x 'sudo ls' main`, [][]string{sudoLs}, nil},
		{`git rebase --exec='sudo ls' main`, [][]string{sudoLs}, nil},
		{`git rebase -ix'sudo ls' main`, [][]string{sudoLs}, nil},
		{`git rebase main --exe 'sudo ls'`, [][]string{sudoLs}, nil},
		{`git -C repo rebase --onto x main -x 'sudo ls'`, [][]string{sudoLs}, nil},
		{`git submodule foreach 'sudo ls'`, [][]string{sudoLs}, nil},
		{`git submodule --quiet foreach --recursive sudo ls`, [][]string{sudoLs}, nil},
		{`git bisect run sudo ls`, [][]string{sudoLs}, nil},
		{`git filter-branch --tree-filter 'sudo ls' HEAD`, [][]string{sudoLs}, nil},
		{`git filter-branch -f -d /tmp/x --index-filter 'sudo ls' -- --all`, [][]string{sudoLs}, nil},
		{`git filter-branch --msg-filter 'sudo ls'; git filter-branch --env-filter 'sudo ls'`, [][]string{sudoLs}, nil},
		{`git filter-branch --commit-filter 'sudo ls' --tag-name-filter cat --setup 'sudo ls'`, [][]string{sudoLs, {"cat"}}, nil},
		{`git difftool -x 'sudo ls'; git difftool --extcmd='sudo ls' HEAD`, [][]string{sudoLs}, nil},
		{`git grep -O'sudo ls' foo; git grep --open-files-in-pager='sudo ls' foo`, [][]string{sudoLs}, nil},
		{`git fetch --upload-pack='sudo ls' .`, [][]string{sudoLs}, nil},
		{`git pull --upload-pack 'sudo ls' .`, [][]string{sudoLs}, nil},
		{`git ls-remote --upload-pack='sudo ls' .; git ls-remote --exec='sudo ls' .`, [][]string{sudoLs}, nil},
		{`git push --receive-pack='sudo ls' . HEAD; git push --exec='sudo ls' . HEAD`, [][]string{sudoLs}, nil},
		{`git send-pack --receive-pack='sudo ls' . main; git fetch-pack --upload-pack='sudo ls' .`, [][]string{sudoLs}, nil},
		{`git archive --remote=. --exec='sudo ls' HEAD`, [][]string{sudoLs}, nil},
		{`git daemon --access-hook='sudo ls' --export-all .`, [][]string{sudoLs}, nil},
		{`git instaweb --httpd='sudo ls'`, [][]string{sudoLs}, nil},
		// send-email reads its options as Getopt::Long: in any case, by a
		// prefix, after "-" or "+" too.
		{`git send-email --to-cmd='sudo ls' x.patch`, [][]string{sudoLs}, nil},
		{`git send-email x.patch --cc-cmd 'sudo ls'`, [][]string{sudoLs}, nil},
		{`git send-email --sendmail-cmd='sudo ls' x.patch`, [][]string{sudoLs}, nil},
		{`git send-email -header-cmd='sudo ls' x.patch`, [][]string{sudoLs}, nil},
		{`git send-email +TO-CM 'sudo ls' x.patch`, [][]string{sudoLs}, nil},
		{`git send-email --smtp-server=/usr/bin/sudo x.patch`, [][]string{{"/usr/bin/sudo"}}, nil},
		// An alias of -c may lead to one of them.
		{`git -c alias.r=rebase r -x 'sudo ls' main`, [][]string{sudoLs}, nil},
		{`git -c alias.a=b -c alias.b='rebase -x' a 'sudo ls' main`, [][]string{sudoLs}, nil},
		{`git -c 'alias.r=rebase #' r -x 'sudo ls'`, [][]string{sudoLs}, nil},
		{`git -c alias.R='rebase -x "sudo ls"' r main`, [][]string{sudoLs}, nil},

		// A word made at run time may be the code or an option with it.
		{`git rebase -x "$c" main`, nil, []string{"computed"}},
		{`git rebase --exec="$c" main`, nil, []string{"computed"}},
		{`git rebase "$base"`, nil, []string{"computed"}},
		{`git rebase --onto $b main`, nil, []string{"computed"}},
		{`git clone "$u"`, nil, []string{"computed"}},
		{`git clone --depth 1 "$u" d`, nil, []string{"computed"}},
		{`git init "$d"`, nil, []string{"computed"}},
		{`git push origin "$b"`, nil, []string{"computed"}},
		{`git fetch $remote`, nil, []string{"computed"}},
		{`git fetch --upload-pack "$u" .`, nil, []string{"computed"}},
		{`git grep "$p"`, nil, []string{"computed"}},
		{`git grep -e -- "$p"`, nil, []string{"computed"}},
		{`git bisect run "$cmd"`, nil, []string{"computed"}},
		{`git bisect "$x" sudo ls`, nil, []string{"computed"}},
		{`git submodule "$x" 'sudo ls'`, nil, []string{"computed"}},
		{`git submodule foreach "$c"`, nil, []string{"computed"}},
		{`git filter-branch "$o" 'sudo ls' HEAD`, nil, []string{"computed"}},
		{`git filter-branch --tree-filter "$f"`, nil, []string{"computed"}},
		{`git filter-branch -d $t HEAD`, nil, []string{"computed"}},
		{`git send-email "+$x" a`, nil, []string{"computed"}},
		{`git send-email --smtp-server="$s" a`, nil, []string{"computed"}},
		{`git rebase {-x,sudo} main`, nil, []string{"computed"}},
		// A tool with a "/" is a file git sources.
		{`git difftool -t ../x`, nil, []string{"computed"}},
		{`git mergetool --tool=../../tmp/x`, nil, []string{"computed"}},
		{`git mergetool --toolz ../x`, nil, []string{"computed"}},
		{`git mergetool -t "$t"`, nil, []string{"computed"}},

		// What xargs appends may be such a word.
		{`ls | xargs git`, nil, []string{"computed"}},
		{`find . -print0 | xargs -0 tar -czf x.tgz`, nil, []string{"computed"}},
		{`ls | xargs git rebase`, nil, []string{"computed"}},
		{`xargs git push origin`, nil, []string{"computed"}},
		{`xargs sudo git fetch`, nil, []string{"computed"}},
		{`xargs rsync -a`, nil, []string{"computed"}},
		{`xargs -I{} git rebase -x {} main`, nil, []string{"computed"}},
	} {
		s, err := Parse(c.src, "", "")
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		for _, argv := range c.here {
			if !slices.ContainsFunc(s.Commands, func(cmd []string) bool { return slices.Equal(cmd, argv) }) {
				t.Errorf("%s: %q not among the commands %q", c.src, argv, s.Commands)
			}
		}
		if !slices.Equal(s.Dynamic, c.dynamic) {
			t.Errorf("%s: dynamic %q, want %q", c.src, s.Dynamic, c.dynamic)
		}
	}
}

// The commands of git with code in no word of the line, values of their
// options with none, words made at run time that cannot be options and
// those after "--" add nothing.
func TestGitSubcommandNone(t *testing.T) {
	for _, src := range []string{
		`git rebase main; git rebase -i HEAD~3; git rebase --onto "$b" main topic`,
		`git fetch origin; git fetch --depth "$n" origin; git pull --rebase origin main`,
		`git push origin HEAD:main; git push origin "HEAD:$b"; git push --force-with-lease origin main`,
		`git grep -n -e "$p" -- '*.go'; git grep -n foo -- "$d"; git grep -O foo`,
		`git submodule update --init --recursive; git submodule foreach git pull`,
		`git submodule foreach 'echo $name'`,
		`git bisect start; git bisect run make test; git bisect run ./t.sh "$a"`,
		`git archive -o out.tar HEAD; git ls-remote origin; git clone https://x/y.git d`,
		`git difftool -y -t vimdiff HEAD~1; git mergetool -t meld`,
		`git send-email --to=x@y.z --cc "$c" 0001.patch`,
		`git filter-branch --tree-filter 'rm -f x' HEAD; git filter-branch -d "$t" HEAD`,
		`git -c alias.co=checkout co; git commit -m "$msg"; git log --grep "$x"`,
		`ls | xargs rm; ls | xargs git add; xargs -n 1 git rm`,
	} {
		s, err := Parse(src, "", "")
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if s.Dynamic != nil {
			t.Errorf("%s: commands %q, dynamic %q", src, s.Commands, s.Dynamic)
		}
	}
}

// The rules judge the code of the commands of git as any command.
func TestGitSubcommandPolicy(t *testing.T) {
	rules, err := Load(context.Background(), t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		cmd, want string
	}{
		{`git rebase -x 'sudo ls' main`, Deny},
		{`git submodule foreach 'sudo ls'`, Deny},
		{`git bisect run sudo ls`, Deny},
		{`git filter-branch --tree-filter 'sudo ls' HEAD`, Deny},
		{`git filter-branch --index-filter 'sudo ls' HEAD`, Deny},
		{`git filter-branch --msg-filter 'sudo ls' HEAD`, Deny},
		{`git difftool -x 'sudo ls'`, Deny},
		{`git difftool --extcmd 'sudo ls'`, Deny},
		{`git grep -O'sudo ls' x`, Deny},
		{`git fetch --upload-pack='sudo ls' .`, Deny},
		{`git pull --upload-pack='sudo ls' .`, Deny},
		{`git ls-remote --upload-pack='sudo ls' .`, Deny},
		{`git push --receive-pack='sudo ls' . HEAD`, Deny},
		{`git archive --remote=. --exec='sudo ls' HEAD`, Deny},
		{`git send-email --to-cmd='sudo ls' x.patch`, Deny},
		{`git send-email --cc-cmd='sudo ls' x.patch`, Deny},
		{`git send-email --sendmail-cmd='sudo ls' x.patch`, Deny},
		{`git -c alias.r=rebase r -x 'sudo ls' main`, Deny},
		{`git rebase -x "$c" main`, Ask},
		{`git clone "$u"`, Ask},
		{`ls | xargs git`, Ask},
		{`find . -print0 | xargs -0 tar -czf x.tgz`, Ask},
		{`git rebase main`, Allow},
		{`git fetch origin`, Allow},
		{`ls | xargs rm`, Allow},
	} {
		if d := check(t, rules, bash(c.cmd)); d.Action != c.want {
			t.Errorf("%s: %+v, want %s", c.cmd, d, c.want)
		}
	}
}
