package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/session"
)

// warnCold tells the user, before the first turn of a request, that the
// provider will not read the session from its cache: the turn pays for
// the whole context as a new one, and in a big session that is what the
// bill and the wait are made of. It only tells: the request goes on, and
// whether the next one is made in a new session (aish new) or after a
// summary (aish compact) is the user's to decide. "uncached" is said once
// per session; "expired" cannot repeat by itself, as the turn this request
// makes warms the cache again.
func (a *Agent) warnCold() {
	if a.Cfg.ColdWarnTokens <= 0 {
		return
	}
	now := time.Now()
	tokens := a.contextTokens(a.entries)
	switch coldCache(a.entries, tokens, a.Cfg.CacheMaxAge(), a.Cfg.ColdWarnTokens, now) {
	case "expired":
		last, _ := measured(session.Current(a.entries))
		fmt.Fprintf(a.UI, "%s[aish: provider cache expired (last turn %s ago); this session is ~%s tokens and the request pays for all of them again. aish new starts a fresh one, aish compact sums it up]%s\n",
			dim, age(now.Sub(last.Time)), session.Short(tokens), reset)
	case "uncached":
		if a.coldNoted == a.sess {
			return
		}
		a.coldNoted = a.sess
		fmt.Fprintf(a.UI, "%s[aish: the provider did not read this session from its cache on the last turn (~%s tokens); every request pays in full. aish new starts a fresh session, aish compact sums it up]%s\n",
			dim, session.Short(tokens), reset)
	}
}

// coldCache says why the session's prefix is not in the provider's cache, "" when it is, or when the
// session is too small to matter.
//
// "expired": the last measured turn is older than ttl; each read of the
// cache renews it, so the last turn is the one that counts. "uncached":
// the last turn read nothing from the cache though the one before it,
// both of minTokens or more, was recent enough to have left the prefix
// there, and sent the same (samePrefix): a provider or a model without a
// cache, or messages that change from turn to turn. A turn that sent
// another prefix, or went to another model, wrote the cache anew and was
// to read nothing. ttl 0 checks no expiry and takes any cache to have
// lived. A turn without a time tells nothing.
func coldCache(es []session.Entry, tokens int, ttl time.Duration, minTokens int, now time.Time) (why string) {
	if minTokens <= 0 || tokens < minTokens {
		return ""
	}
	last, prev := measured(session.Current(es))
	if last == nil || last.Time.IsZero() {
		return "" // the first request of a session has nothing to read
	}
	if ttl > 0 && now.Sub(last.Time) > ttl {
		return "expired"
	}
	if prev == nil || prev.Time.IsZero() || prev.InputTokens < minTokens || last.InputTokens < minTokens {
		return ""
	}
	if (ttl == 0 || last.Time.Sub(prev.Time) <= ttl) && last.CachedTokens == 0 && samePrefix(*last, *prev) {
		return "uncached"
	}
	return ""
}

// samePrefix says whether turn b was sent what turn a was before the
// conversation, to the same provider, model and profile: the cache a
// wrote was b's to read. Turns recorded before Entry.Prefix have none, and
// are taken for the same, as they were before.
func samePrefix(a, b session.Entry) bool {
	return a.Prefix == b.Prefix && a.Provider == b.Provider && a.Model == b.Model && a.Profile == b.Profile
}

// measured are the last two assistant turns of es whose input the API
// counted, the last first; nil where there is none.
func measured(es []session.Entry) (last, prev *session.Entry) {
	for i := len(es) - 1; i >= 0; i-- {
		if es[i].Kind != session.KindAssistant || es[i].InputTokens <= 0 {
			continue
		}
		if last != nil {
			return last, &es[i]
		}
		last = &es[i]
	}
	return last, nil
}

// prefixKey is a digest of what the provider caches of req ahead of its
// messages, the system prompt and the tools, and of what the messages are
// made from the journal by: max_output_bytes and the mask; and of the
// effort, which sets the thinking, a change of which may drop the cached
// messages. A turn whose key is not the one of the turn before it writes
// the cache anew, as it should: after `aish apply-config` with another
// system_prompt, policy hint or MCP server, a tool loaded by tool_search,
// a request from a project with a system_prompt, policy hints or tools of
// its own. The date and the directory of a request are in its message.
func (a *Agent) prefixKey(req llm.Request) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	enc.Encode(req.System)
	enc.Encode(req.Tools)
	enc.Encode([]any{a.Cfg.Effort, a.Cfg.MaxOutputBytes, a.maskKey})
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// age is d the short way, in its largest unit, rounded: 40s, 23m, 5h, 3d.
func age(d time.Duration) string {
	if s := d.Round(time.Second); s < time.Minute {
		return fmt.Sprintf("%ds", s/time.Second)
	}
	if m := d.Round(time.Minute); m < time.Hour {
		return fmt.Sprintf("%dm", m/time.Minute)
	}
	const day = 24 * time.Hour
	if h := d.Round(time.Hour); h < day {
		return fmt.Sprintf("%dh", h/time.Hour)
	}
	return fmt.Sprintf("%dd", d.Round(day)/day)
}
