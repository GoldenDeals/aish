package proxy

import (
	"bytes"
	"context"
	"time"
)

// A firm question is a Yes or No the proxy asks for itself, of the user at
// the shell's prompt: rpc yolo asks one before the agent's checks go off.
// The call may come from code the agent left for the shell to run there (a
// trap, PROMPT_COMMAND), past the guard. What makes the answer the user's
// is that the proxy reads it from the terminal, in key: a process in the
// shell writes its keys to the PTY, and they reach the shell, never the
// question. The question comes up where the prompt would, though, and the
// keys the user types ahead for the prompt must not answer it. So No is
// chosen at first; the keys of its first firmWait are dropped, Ctrl+C
// keeping its meaning; and Esc, Alt with a key or any key that is no answer
// to it, a letter of a command typed ahead, answers No, dropping the rest.

// firmWait is how long the keys after a firm question opens are taken for
// keys typed before it showed. A variable: the tests shorten it.
var firmWait = time.Second

// firmAsk marks the ctx askUser gets from confirm.
type firmAsk struct{}

// confirm asks q as a firm question and tells whether the user answered
// Yes. Unanswered, the error is askUser's.
func (p *Proxy) confirm(ctx context.Context, q string) (bool, error) {
	ans, err := p.askUser(context.WithValue(ctx, firmAsk{}, true), q)
	return err == nil && ans == "y", err
}

// firmly makes pr firm when ctx is confirm's. Called under p.mu, before pr
// is drawn.
func (pr *prompt) firmly(ctx context.Context) {
	if ctx.Value(firmAsk{}) != nil {
		pr.yes, pr.firm, pr.from = false, true, time.Now().Add(firmWait)
	}
}

// firmAnswers are the keys askKey reads from a firm question as they are;
// the arrows, sequences, it reads too.
var firmAnswers = []byte("yYnNhl\r\n\t\x03\x0f")

// firmKeys is what of the keys b askKey reads for pr: all of them unless pr
// is firm. Called under p.mu.
func (pr *prompt) firmKeys(b []byte) []byte {
	if !pr.firm {
		return b
	}
	if time.Now().Before(pr.from) {
		if bytes.IndexByte(b, 0x03) >= 0 {
			return []byte{0x03}
		}
		return nil
	}
	keys := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case c == 0x1b && i+1 < len(b) && (b[i+1] == '[' || b[i+1] == 'O'):
			j := i + 2
			for b[i+1] == '[' && j < len(b) && (b[j] < 0x40 || b[j] > 0x7e) {
				j++
			}
			j = min(j, len(b)-1)
			keys = append(keys, b[i:j+1]...)
			i = j
		case bytes.IndexByte(firmAnswers, c) >= 0:
			keys = append(keys, c)
		default:
			return append(keys, 'n')
		}
	}
	return keys
}
