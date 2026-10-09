package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/tools"
)

// The viewer shows the agent's calls of the whole session. The request
// whose folds the proxy keeps, the current or last one until dropFolds (the
// next request, Ctrl+L between requests, `aish resume`), shows those folds:
// the calls that were folded, their output whole as it came. The requests
// before it come from the journal, every call with its result as the model
// got it: cut to max_output_bytes, a command's ended by its exit code. The
// journal is read again only once it changed: in a long session reading it
// would cost each tick of the open viewer.

// journalAt is a place in a journal: the journal of session id, n entries
// long.
type journalAt struct {
	id string
	n  int
}

// history is the viewer's reading of the journal, kept while the journal
// and the place where the request of p.folds began stay the same.
type history struct {
	read  journalAt // the journal read
	from  int       // where in it the request of p.folds began
	calls []Fold    // the calls before from, see pastCalls
	// ask is the text of the request of p.folds, if the journal has it:
	// the line above its folds.
	ask   string
	asked bool
}

// dropFolds forgets the folds of the last request: from here on the
// viewer shows its calls from the journal, as those of the requests before
// it. Called under p.mu.
func (p *Proxy) dropFolds() {
	p.folds = nil
	p.foldsAt = journalAt{p.sess.ID, p.sess.Len()}
}

// viewFolds are the agent's calls of the session: those of the journal,
// then the folds of the current or last request, including the one being
// printed. The open viewer takes them anew on every tick. Called under
// p.mu.
func (p *Proxy) viewFolds() []Fold {
	h := p.readHistory()
	folds := h.calls[:len(h.calls):len(h.calls)]
	// The folds of a session left by `aish clear` are not this one's.
	if p.foldsAt.id != "" && p.foldsAt.id != p.sess.ID {
		return folds
	}
	live := p.liveFolds()
	if len(live) == 0 {
		return folds
	}
	if h.asked {
		folds = append(folds, askFold(h.ask))
	}
	return append(folds, live...)
}

// liveFolds are the folds of the current or last request, including the
// one being printed.
func (r *recorder) liveFolds() []Fold {
	folds := r.folds[:len(r.folds):len(r.folds)]
	if f := r.liveFold(); f != nil && !f.open {
		// A command cut short on the screen is there before it prints anything.
		if raw := f.raw.Bytes(); len(raw) > 0 || f.cut() {
			folds = append(folds, Fold{Title: f.title + "  (running)", Text: string(raw)})
		}
	}
	return folds
}

// readHistory is the journal as the viewer shows it, read again if it grew
// or changed, or the folds began elsewhere. Called under p.mu.
func (p *Proxy) readHistory() *history {
	h := &p.hist
	// The folds of another session, or none yet: all of this one's journal.
	ours := p.foldsAt.id == p.sess.ID
	at := journalAt{p.sess.ID, p.sess.Len()}
	from := at.n
	if ours {
		from = min(p.foldsAt.n, at.n)
	}
	if h.read == at && h.from == from && h.calls != nil {
		return h
	}
	es := p.sess.Entries() // perhaps longer by now than Len said
	if !ours {
		from = len(es)
	}
	h.read, h.from = journalAt{at.id, len(es)}, from
	h.calls, h.ask, h.asked = pastCalls(es, from)
	return h
}

// pastCalls are the agent's calls in es before from, as the viewer shows
// them: the calls of a request after a line with its text, a cleared
// screen and a summary marked where they are. A request without calls
// shows nothing. ask is the text of the last request if none of its calls
// is there: the request of the folds, which begins at from.
func pastCalls(es []session.Entry, from int) (calls []Fold, ask string, asked bool) {
	calls = []Fold{}
	// The calls of the last reply, by id: their results come after it.
	// Ids are looked up there alone, as some providers count them anew.
	pending := map[string]int{}
	for i, e := range es {
		switch e.Kind {
		case session.KindUser:
			ask, asked = e.Text, true
		case session.KindClear, session.KindSummary:
			if i >= from || len(calls) == 0 {
				continue
			}
			mark := Fold{Text: "── screen cleared ──"}
			if e.Kind == session.KindSummary {
				mark.Text = "── compacted ──"
			}
			if calls[len(calls)-1] != mark {
				calls = append(calls, mark)
			}
		case session.KindAssistant:
			clear(pending)
			if i >= from {
				continue // the request of the folds
			}
			for _, c := range e.ToolCalls {
				if asked {
					calls = append(calls, askFold(ask))
					asked = false
				}
				pending[c.ID] = len(calls)
				calls = append(calls, Fold{Title: callTitle(c)})
			}
		case session.KindToolResult:
			// The result of a call cut short comes with the next request:
			// it is the result of a call before from all the same.
			if j, ok := pending[e.ToolCallID]; ok {
				calls[j].Text = e.Output
				delete(pending, e.ToolCallID)
			}
		}
	}
	return calls, ask, asked
}

// askFold is the line of the viewer above the calls of a request, its
// text: a fold without a title, see newViewPart.
func askFold(text string) Fold {
	text = strings.TrimRight(text, "\n")
	return Fold{Text: "? " + strings.ReplaceAll(text, "\n", "\n  ")}
}

// builtins are the built-in tools by name, for the titles of their calls.
var builtins = func() map[string]tools.Tool {
	m := map[string]tools.Tool{}
	for _, t := range tools.Builtins() {
		m[t.Name()] = t
	}
	return m
}()

// callTitle is the title of call c, as its fold has it on the screen: a
// bash command after ❯, any other call after ⚙. A tool that is not a
// built-in one is not at hand: its arguments come in the order the model
// wrote them, not the order of the tool's.
func callTitle(c session.ToolCall) string {
	args, _ := tools.Decode(c.Args)
	if c.Name == tools.Bash {
		cmd, _ := args["command"].(string)
		return "❯ " + cmd
	}
	if t, ok := builtins[c.Name]; ok {
		return "⚙ " + tools.Title(t, args)
	}
	return "⚙ " + strings.Join(append([]string{c.Name}, argWords(c.Args)...), " ")
}

// argWords are the values of the arguments raw, in their order, each as
// tools.Title puts it.
func argWords(raw json.RawMessage) []string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var words []string
	for dec.More() {
		if _, err := dec.Token(); err != nil {
			break
		}
		var v any
		if err := dec.Decode(&v); err != nil {
			break
		}
		s := fmt.Sprint(v)
		if len(s) > 80 || strings.Contains(s, "\n") {
			s = fmt.Sprintf("<%d bytes>", len(s))
		}
		words = append(words, s)
	}
	return words
}
