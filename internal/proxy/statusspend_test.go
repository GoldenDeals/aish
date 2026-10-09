package proxy

import (
	"testing"

	"github.com/GoldenDeals/aish/internal/session"
)

// The status sums the subagents' turns apart from the host's, and they
// do not change the size of the context.
func TestStatusSubagentSpend(t *testing.T) {
	sess, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := New(sess)
	if err := sess.Append(
		session.Entry{Kind: session.KindUser, Text: "q"},
		session.Entry{Kind: session.KindAssistant, Text: "a", InputTokens: 7000, CachedTokens: 5000, OutputTokens: 300},
	); err != nil {
		t.Fatal(err)
	}
	before := p.status()
	if err := sess.Append(
		session.Entry{Kind: session.KindUsage, About: "explore", Usage: &session.Usage{Input: 2000, Cached: 1500, Output: 80}},
		session.Entry{Kind: session.KindUsage, About: "explore", Usage: &session.Usage{Input: 3000, Output: 20}},
		session.Entry{Kind: session.KindUsage, About: "old"}, // no Usage: counts nothing
	); err != nil {
		t.Fatal(err)
	}
	st := p.status()
	if st.InputTokens != 7000 || st.CachedTokens != 5000 || st.OutputTokens != 300 {
		t.Errorf("host %d/%d/%d, want 7000/5000/300", st.InputTokens, st.CachedTokens, st.OutputTokens)
	}
	if st.SubInputTokens != 5000 || st.SubCachedTokens != 1500 || st.SubOutputTokens != 100 {
		t.Errorf("subagents %d/%d/%d, want 5000/1500/100", st.SubInputTokens, st.SubCachedTokens, st.SubOutputTokens)
	}
	if st.Tokens != before.Tokens || st.Measured != before.Measured || st.Tokens != 7300 {
		t.Errorf("context %d (measured %v), before the usage %d (%v)", st.Tokens, st.Measured, before.Tokens, before.Measured)
	}
}
