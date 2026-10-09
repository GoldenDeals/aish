package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
)

// titleProv answers with reply, or fails with err, and keeps the request.
type titleProv struct {
	reply string
	err   error
	req   llm.Request
}

func (p *titleProv) Name() string                                    { return "fake" }
func (p *titleProv) Model() string                                   { return "m" }
func (p *titleProv) Efforts() []string                               { return nil }
func (p *titleProv) MaxTokens(string) int64                          { return 0 }
func (p *titleProv) Models(context.Context) ([]llm.ModelInfo, error) { return nil, nil }
func (p *titleProv) Complete(_ context.Context, req llm.Request, _ func(string)) (*llm.Response, error) {
	p.req = req
	if p.err != nil {
		return nil, p.err
	}
	return &llm.Response{Text: p.reply}, nil
}

func TestTitle(t *testing.T) {
	cfg := config.Default()
	prov := &titleProv{reply: "\n  **Title: \"Fix the nginx config.\"**  \nbecause it is broken"}
	text := "fix nginx, password=hunter2hunter2 " + strings.Repeat("x", 10000)
	got, err := Title(context.Background(), prov, cfg, text)
	if err != nil || got != "Fix the nginx config" {
		t.Fatalf("%q %v", got, err)
	}
	sent := prov.req.Messages[0].Text
	if strings.Contains(sent, "hunter2") || len(sent) > titleText+100 || prov.req.System == "" || len(prov.req.Tools) > 0 {
		t.Errorf("sent %d bytes: %.80q…", len(sent), sent)
	}

	for _, reply := range []string{"", " \n\"\" "} {
		prov.reply = reply
		if got, err := Title(context.Background(), prov, cfg, "hi"); err == nil {
			t.Errorf("%q: named %q", reply, got)
		}
	}
	prov.err = errors.New("overloaded")
	if _, err := Title(context.Background(), prov, cfg, "hi"); err == nil {
		t.Error("an error of the provider")
	}
}

func TestCleanTitle(t *testing.T) {
	long := strings.Repeat("слово ", 20)
	for in, want := range map[string]string{
		"Разбор логов nginx":                  "Разбор логов nginx",
		"`Deploy to staging`.":                "Deploy to staging",
		"# Title:  Disk   usage\n\nmore text": "Disk usage",
		long:                                  "слово слово слово слово слово слово слово слово слово слово",
		strings.Repeat("я", 70):               strings.Repeat("я", 60),
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
