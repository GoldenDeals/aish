package agent

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
)

const titlePrompt = `You name a terminal session after the first request the user made in it, the way a chat is titled in a list of chats. Reply with the name only: 2 to 6 words saying what the session is about, in the language of the request, sentence case, no quotes, no final period. Do not answer the request.`

// titleText is how much of the request goes for a name: its start says
// what it is about, and a pasted log after it would cost for nothing.
const titleText = 4000

// titleRunes is as long as a name may be (session.CheckName).
const titleRunes = 60

// Title asks prov for a short name of a session whose first request was
// text: what `aish resume --all` lists the session by if the user did not
// name it. The text goes masked by cfg, as the rest of what the model is
// sent; the reply is made one line of at most 60 characters.
func Title(ctx context.Context, prov llm.Provider, cfg config.Config, text string) (string, error) {
	mask, err := NewMasker(cfg.MaskDefaults, cfg.Mask)
	if err != nil { // config.Load rejects these; keep the built-in ones
		mask, _ = NewMasker(true, nil)
	}
	if len(text) > titleText {
		text = strings.ToValidUTF8(text[:titleText], "")
	}
	req := llm.Request{
		System:   titlePrompt,
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "<request>\n" + mask.Mask(text) + "\n</request>"}},
	}
	resp, err := prov.Complete(ctx, req, nil)
	if err != nil {
		return "", err
	}
	title := cleanTitle(resp.Text)
	if title == "" {
		return "", errors.New("the model gave no name")
	}
	return title, nil
}

// cleanTitle makes a name of the model's reply: its first line, without
// the quotes, the markdown and the "Title:" models dress a name in, cut
// at a word to titleRunes.
func cleanTitle(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if s = strings.TrimSpace(line); s != "" {
			break
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	for {
		before := s
		s = strings.Trim(s, " \"'`*_#«»“”„")
		if head, rest, ok := strings.Cut(s, ":"); ok && strings.EqualFold(strings.TrimSpace(head), "title") {
			s = rest
		}
		s = strings.TrimSuffix(s, ".")
		if s == before {
			break
		}
	}
	if utf8.RuneCountInString(s) <= titleRunes {
		return s
	}
	r := []rune(s)[:titleRunes]
	if i := strings.LastIndex(string(r), " "); i > 0 {
		return strings.TrimSpace(string(r)[:i])
	}
	return string(r)
}
