package proxy

import (
	"path/filepath"
	"testing"

	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

func TestAishOnly(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{
		{"aish status", true},
		{"aish context --full", true},
		{"aish context --full > x.jsonl", true},
		{"aish status | cat", true},
		{"aish session show | less -R", true},
		{`"$AISH_BIN" status`, true},
		{"$AISH_BIN status", true},
		{"${AISH_BIN} model", true},
		{"aish session show", true},
		{"/usr/local/bin/aish status", true},
		{"AISH_PROFILE=work aish status", true},
		{"aish status && aish context", true},
		{"aish clear; aish status", true},
		{"aish status\naish tasks", true},
		{"aish yolo", true}, // a subcommand aish has not got yet is still its own
		{"aish", true},
		{"aish tool weather Paris", false},
		{"aish tool weather Paris | cat", false},
		{"aish status; aish tool weather Paris", false},
		{"make; aish status", false},
		{"ls && aish context", false},
		{"ls | aish status", false},
		{"ls", false},
		{"echo aish", false},
		{"aishx", false},
		{"aishx status", false},
		{`aish "$sub"`, false}, // may be tool
		{"aish 'tool' x", false},
		{"$X status", false},
		{`"$AISH_BIN:x" status`, false},
		{"(aish status)", false},
		{`aish status "`, false}, // bash cannot parse it
		{"", false},
	} {
		if got := aishOnly(tc.line); got != tc.want {
			t.Errorf("%q: aish only %v, want %v", tc.line, got, tc.want)
		}
	}
}

// What the user's aish commands print stays out of the journal, `aish
// tool` aside; a clear's own line opens no new session.
func TestAishCommandsUnrecorded(t *testing.T) {
	sess, err := session.New(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.out = &terminal{}
	ctx := asUser(p)
	run := func(cmd, output string, during func()) {
		p.marker(Marker{Kind: "cmd-start", Payload: cmd})
		if during != nil {
			during()
		}
		p.output([]byte(output))
		p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
	}
	entries := func() []session.Entry {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.sess.Entries()
	}
	run("aish status", "model: m\r\n", nil)
	run("aish context --full > ctx.jsonl", "", nil)
	if es := entries(); len(es) != 0 {
		t.Fatalf("aish commands recorded: %+v", es)
	}
	run("aish tool weather Paris", "sunny\r\n", nil)
	run("ls", "a b\r\n", nil)
	es := entries()
	if len(es) != 2 || es[0].Cmd != "aish tool weather Paris" || es[0].Output != "sunny" || es[1].Cmd != "ls" || es[1].Output != "a b" {
		t.Fatalf("journal: %+v", es)
	}

	old := sess.ID
	run("aish clear", "", func() {
		if _, err := p.handle(ctx, rpc.MethodClear, nil); err != nil {
			t.Fatal(err)
		}
	})
	p.mu.Lock()
	id := p.sess.ID
	p.mu.Unlock()
	if id == old {
		t.Fatal("aish clear kept the session")
	}
	if es := entries(); len(es) != 0 {
		t.Errorf("the new session starts with %+v", es)
	}
}
