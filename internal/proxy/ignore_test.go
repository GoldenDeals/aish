package proxy

import (
	"path/filepath"
	"testing"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/session"
)

func TestIgnoredCommand(t *testing.T) {
	patterns := config.Default().JournalIgnore
	for _, tc := range []struct {
		line string
		want bool
	}{
		{"env", true},
		{"printenv", true},
		{"/usr/bin/env", true},
		{"sudo env", true},
		{"sudo -u root env", true},
		{"env | grep PATH", true},                      // a pipeline with a matching command
		{"cd /tmp && history", true},                   // a list with one
		{"bash -c 'env'", true},                        // behind a shell
		{"cat ~/.aws/credentials", true},               // `*` crosses the slashes
		{"cat /etc/app/credentials.json | jq .", true}, // and matches the whole command
		{"kubectl get secret tok -o yaml", true},       // `*secret*` anywhere in it
		{"echo \"a secret", true},                      // unparsable: the whole line is matched
		{"history", true},                              // the pattern needs the command whole
		{"env FOO=1 ./run", false},                     // so a wrapper with a command is not `env`
		{"echo env", false},                            // nor an argument
		{"echo \"history", false},                      // nor an unparsable line that is not it
		{"ls", false},
		{"grep -r PATH .", false},
		{"cat secrets.txt", true},
		{"vim ~/.aws/config", false},
	} {
		if got := ignoredCommand(tc.line, patterns); got != tc.want {
			t.Errorf("%q: ignored %v, want %v", tc.line, got, tc.want)
		}
	}
	if ignoredCommand("env", nil) {
		t.Error("an empty list ignores nothing")
	}
	if !ignoredCommand("git log", []string{"git *"}) || ignoredCommand("git log", []string{"git"}) {
		t.Error("patterns match the command whole, as HISTIGNORE does")
	}
}

// An ignored command reaches the journal without its output; another with it.
func TestJournalIgnore(t *testing.T) {
	sess, err := session.New(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.out = &terminal{}
	p.ignore = config.Default().JournalIgnore
	run := func(cmd, output string) {
		p.marker(Marker{Kind: "cmd-start", Payload: cmd})
		p.output([]byte(output))
		p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
	}
	run("export FOO_TOKEN=x; env", "FOO_TOKEN=x\r\nHOME=/home/u\r\n")
	run("ls", "a b\r\n")
	es := sess.Entries()
	if len(es) != 2 {
		t.Fatalf("journal: %+v", es)
	}
	if es[0].Cmd != "export FOO_TOKEN=x; env" || es[0].Output != session.NotRecorded || es[0].Exit != 0 || es[0].Cwd != "/tmp" {
		t.Errorf("ignored command: %+v", es[0])
	}
	if es[1].Cmd != "ls" || es[1].Output != "a b" {
		t.Errorf("other command: %+v", es[1])
	}
}
