package proxy

import (
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/session"
)

// A shell on the top level of config.toml, which selects a profile, goes
// to another endpoint than the shell next to it: the status says so.
func TestStatusNamesRoot(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	p.model, p.profile, p.defProfile = "m", "", "work"
	if text, _ := p.statusText(); !strings.HasPrefix(text, "root · ") {
		t.Errorf("the top level under profile work: %q", text)
	}
	p.defProfile = ""
	if text, _ := p.statusText(); text != "m" {
		t.Errorf("the top level config.toml selects: %q", text)
	}
	p.profile = "local"
	if text, _ := p.statusText(); text != "local · m" {
		t.Errorf("a profile over the top level: %q", text)
	}
}
