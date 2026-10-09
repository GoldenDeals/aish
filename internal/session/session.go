// Package session is the journal of one aish shell: commands with their
// output, user requests, assistant turns and tool results. Next to it are
// kept the shell's state, so that `aish resume` brings back the shell along
// with what the assistant knows, and the session's name.
package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	KindShell      = "shell"       // a command the user ran
	KindUser       = "user"        // a natural-language request
	KindAssistant  = "assistant"   // an LLM turn (text and/or tool calls)
	KindToolResult = "tool_result" // the result of one tool call
	// KindInstructions is an instruction file (CLAUDE.md) read when the user
	// first asked something from its directory.
	KindInstructions = "instructions"
	// KindFile is a file the user mentioned as @path in a request.
	KindFile = "file"
	// KindSkill is a skill the user invoked as /name in a request.
	KindSkill = "skill"
	// KindContext is what a user-prompt hook added to the request (About:
	// the hook's name): kept apart from what the user typed.
	KindContext = "context"
	// KindSummary replaces everything before it: `aish compact` asked the
	// model to sum the session up, and only the summary is sent from then on.
	KindSummary = "summary"
	// KindClear marks where the user erased the screen: the model is sent
	// only what follows.
	KindClear = "clear"
	// KindUsage is what a call to the model cost whose context is not the
	// session's: a turn of a subagent (About: its name). It is there for
	// aish stats alone: the model is not sent it, Tokens does not count it,
	// and its tokens are in Usage, not in InputTokens and the rest, which
	// tell where the API measured the session's own context.
	KindUsage = "usage"
)

// NotRecorded is the Output of a shell command that journal_ignore matched:
// the journal, and so the model, know the command ran, not what it printed.
const NotRecorded = "[not recorded]"

type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type Entry struct {
	Kind string    `json:"kind"`
	Time time.Time `json:"time"`

	// shell
	Cmd    string `json:"cmd,omitempty"`
	Output string `json:"output,omitempty"`
	Exit   int    `json:"exit,omitempty"`
	Cwd    string `json:"cwd,omitempty"`
	TUI    bool   `json:"tui,omitempty"` // a full-screen program as a whole: Output is its one line

	// user, assistant; context: what the hook added
	Text string `json:"text,omitempty"`
	// user: the top of the git repository Cwd is in, "" outside one (and
	// in journals from before the field). The request's header has it, not
	// the system prompt, which a cd would change.
	Repo string `json:"repo,omitempty"`

	// assistant
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// Raw is the provider's own encoding of the assistant message (thinking
	// blocks etc.), replayed verbatim when the same provider, model and
	// profile are used.
	Raw      json.RawMessage `json:"raw,omitempty"`
	Provider string          `json:"provider,omitempty"`
	Model    string          `json:"model,omitempty"`
	// Profile is the one of config.toml the reply came from, "" for its top
	// level: Raw is replayed only within it, as accounts differ between
	// profiles.
	Profile string `json:"profile,omitempty"`
	// Prefix is a digest of what the request for this turn sent before the
	// conversation, the system prompt and the tools, and of what the
	// conversation was rendered by: a turn with the prefix, provider, model
	// and profile of the turn before it could read what that one cached.
	Prefix string `json:"prefix,omitempty"`
	// What the request for this turn cost: everything sent, cache included,
	// the part of it read from the cache, and the reply.
	InputTokens  int `json:"input_tokens,omitempty"`
	CachedTokens int `json:"cached_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
	// DroppedTokens is the part of OutputTokens the next request does not
	// send, the reasoning of a provider that cannot take it back: paid
	// for, not in the context.
	DroppedTokens int `json:"dropped_tokens,omitempty"`

	// instructions, file, skill (About: the skill's name), context (About:
	// the hook's name)
	Path  string `json:"path,omitempty"`
	About string `json:"about,omitempty"`

	// tool_result
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`

	// tool_result, file, skill (a mention that could not be read)
	IsError bool `json:"is_error,omitempty"`

	// usage (Provider, Model and Profile are of the call too)
	Usage *Usage `json:"usage,omitempty"`
}

// Usage is what a call to the model cost, counted as for an assistant
// turn: everything sent, cache included, the part read from the cache, the
// reply.
type Usage struct {
	Input  int `json:"input"`
	Cached int `json:"cached,omitempty"`
	Output int `json:"output"`
}

// Session is safe for concurrent use. Every session is saved: its first
// entry puts it on disk, locked, and each entry after it is persisted to
// the JSONL file at once. One without entries has nothing to resume and
// leaves no file.
type Session struct {
	mu      sync.Mutex
	ID      string
	path    string
	entries []Entry
	lock    *os.File
	// saved: the journal is on disk, Append writes through.
	saved bool
	// name is the one the user gave the session: on disk next to the
	// journal, kept here till there is one.
	name string
	// bad counts the journal lines Open could not parse, so that lost
	// entries do not go unnoticed.
	bad int
}

func New(dir string) (*Session, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	id := freshID(dir, "")
	return &Session{ID: id, path: filepath.Join(dir, id+".jsonl")}, nil
}

// Latest opens the most recently modified session in dir that no other
// aish has open and List shows, as newestClosed finds it: of the other
// journals at most the start is read.
func Latest(dir string) (*Session, error) {
	if id := newestClosed(dir); id != "" {
		return Load(dir, id)
	}
	return New(dir)
}

func Open(path string) (*Session, error) {
	id := trimExt(filepath.Base(path))
	if err := CheckID(id); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := &Session{ID: id, path: path, saved: true, name: readName(filepath.Join(filepath.Dir(path), id+".name"))}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			s.bad++
			continue
		}
		s.entries = append(s.entries, e)
	}
	return s, sc.Err()
}

// BadLines is how many lines of the journal Open skipped as unparsable.
func (s *Session) BadLines() int { return s.bad }

func (s *Session) Append(es ...Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(es) == 0 {
		return nil
	}
	// Kept whatever the disk says: the agent goes on from them.
	from := len(s.entries)
	for _, e := range es {
		if e.Time.IsZero() {
			e.Time = time.Now()
		}
		s.entries = append(s.entries, e)
	}
	if !s.saved {
		// The first entry: now there is something to resume. A save that
		// failed is tried again with the next one, all entries so far.
		return s.save()
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, e := range s.entries[from:] {
		b, _ := json.Marshal(e)
		if _, err := f.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// Save puts the session on disk before its first entry, which would put
// it there anyway: the entries so far at once, later ones as they are
// appended. The session is locked from here, like one that was opened.
func (s *Session) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saved {
		return nil
	}
	return s.save()
}

// save is Save under s.mu.
func (s *Session) save() error {
	dir := filepath.Dir(s.path)
	// Locked first: a journal on disk is there for another aish to open.
	l, err := lock(dir, s.ID)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, e := range s.entries {
		b, _ := json.Marshal(e)
		buf.Write(append(b, '\n'))
	}
	if err := os.WriteFile(s.path, buf.Bytes(), 0o600); err != nil {
		unlock(l)
		return err
	}
	s.lock, s.saved = l, true
	if s.name != "" {
		return writeName(dir, s.ID, s.name)
	}
	return nil
}

// Saved reports whether the journal is on disk: the session has had an
// entry, or was saved before it.
func (s *Session) Saved() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved
}

func (s *Session) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Entry(nil), s.entries...)
}

// Next is a new session in the directory of s, for the shell to go on in
// when it leaves s: s stays as it is, on disk and locked if it was, for
// its holder to unlock. Its id is not s's, which a session made the same
// second would share while s has no file yet.
func (s *Session) Next() *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Dir(s.path)
	id := freshID(dir, s.ID)
	return &Session{ID: id, path: filepath.Join(dir, id+".jsonl")}
}

// freshID names a journal that is neither cur nor on disk: two clears within
// a second would otherwise both write to one file.
func freshID(dir, cur string) string {
	id := fmt.Sprintf("%s-%d", time.Now().Format("20060102-150405"), os.Getpid())
	for n, base := 2, id; ; n++ {
		if _, err := os.Stat(filepath.Join(dir, id+".jsonl")); id != cur && err != nil {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
}

func (s *Session) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// LastCwd is the directory of the last command recorded, if any.
func (s *Session) LastCwd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.entries) - 1; i >= 0; i-- {
		if s.entries[i].Cwd != "" {
			return s.entries[i].Cwd
		}
	}
	return ""
}

// Current is the part of the journal the model is sent: from the last
// summary on, or what follows the last clear, whichever is later.
func Current(es []Entry) []Entry {
	for i := len(es) - 1; i >= 0; i-- {
		switch es[i].Kind {
		case KindSummary:
			return es[i:]
		case KindClear:
			return es[i+1:]
		}
	}
	return es
}

// Estimate is the size of the context the next request sends, in tokens.
type Estimate struct {
	Tokens int
	// Measured: a turn since the last summary or clear was measured, and
	// Tokens starts from what the API counted.
	Measured bool
	// PerToken is how many bytes of the journal a token takes: what the
	// API has not counted yet is estimated by it.
	PerToken float64
}

const (
	// defaultPerToken is the bytes a token is taken to take till the API
	// has counted enough of the session: about what code and command
	// output take with Claude's tokenizers. Prose takes 3–4, JSON and
	// listings 2, random text such as base64 one.
	defaultPerToken = 3.0
	// The measure is taken once the API has counted minMeasured tokens of
	// the journal's own, from steps of between minPerToken and maxPerToken
	// bytes a token: past those the count is of something the bytes do not
	// hold.
	minMeasured              = 1000
	minPerToken, maxPerToken = 1.0, 8.0
)

// Tokens estimates the size of the context the next request sends: what
// the last turn measured, all it was sent and its reply but the reasoning
// its provider does not send back, and what came after it at PerToken
// bytes a token. Till a turn since the last summary or clear is measured,
// the whole context is estimated so, with overhead: the bytes of the
// system prompt and the tool schemas, which the API counts into
// InputTokens and the journal does not hold. Shell output counts up to
// maxOutput bytes, as much as the model is sent. An empty context stays
// empty: there is nothing to send yet.
//
// The status at the prompt, `aish context` and the agent's compact_at all
// count by it, with the max_output_bytes and the overhead of the agent.
func Tokens(es []Entry, maxOutput, overhead int) Estimate {
	est := Estimate{PerToken: perToken(es, maxOutput)}
	cur := Current(es)
	from := 0
	if i := lastMeasured(cur); i >= 0 {
		est.Tokens, est.Measured, from = cur[i].InputTokens+kept(cur[i]), true, i+1
	}
	bytes := 0
	for _, e := range cur[from:] {
		bytes += EntryBytes(e, maxOutput)
	}
	if !est.Measured {
		if bytes == 0 {
			return est
		}
		bytes += overhead
	}
	est.Tokens += int(float64(bytes) / est.PerToken)
	return est
}

// lastMeasured is the index of the last turn of es whose input the API
// counted, -1 if there is none.
func lastMeasured(es []Entry) int {
	for i := len(es) - 1; i >= 0; i-- {
		if es[i].Kind == KindAssistant && es[i].InputTokens > 0 {
			return i
		}
	}
	return -1
}

// kept is what the next request sends of the reply of the turn e.
func kept(e Entry) int { return e.OutputTokens - min(e.DroppedTokens, e.OutputTokens) }

// perToken is how many bytes of the journal a token takes with the model
// of the last measured turn, as the API counted the session: from one
// measured turn of a request to the next, the input grew by the first
// one's reply and by the entries between them, results of tools mostly,
// and their bytes over those tokens is the measure. Within a request the
// reasoning of its turns stays, or the provider says it drops it
// (DroppedTokens); between requests some models drop it unsaid, so no
// step across one is taken, nor one that changed what was sent before the
// conversation (Prefix: tools tool_search loaded, say).
// The tokenizer is the model's, the mix of code, output and prose the
// session's. defaultPerToken till minMeasured tokens are counted so.
func perToken(es []Entry, maxOutput int) float64 {
	last := lastMeasured(es)
	if last < 0 {
		return defaultPerToken
	}
	model := es[last].Provider + "\x00" + es[last].Model
	bytes, tokens := 0, 0
	prev, between := -1, 0
	for i, e := range es {
		switch {
		case e.Kind == KindAssistant && e.InputTokens > 0:
			if prev >= 0 && e.Prefix == es[prev].Prefix && e.Provider+"\x00"+e.Model == model {
				t := e.InputTokens - es[prev].InputTokens - kept(es[prev])
				if r := float64(between) / float64(t); t > 0 && r >= minPerToken && r <= maxPerToken {
					bytes, tokens = bytes+between, tokens+t
				}
			}
			prev, between = i, 0
		case e.Kind == KindAssistant || e.Kind == KindUser || e.Kind == KindSummary || e.Kind == KindClear:
			// A turn the API did not count, or another request or
			// context: no step from the last turn to the next.
			prev, between = -1, 0
		default:
			between += EntryBytes(e, maxOutput)
		}
	}
	if tokens < minMeasured {
		return defaultPerToken
	}
	return float64(bytes) / float64(tokens)
}

// EntryBytes is what Tokens counts for e: its command, text and output,
// shell output up to maxOutput bytes, the arguments of its tool calls, and
// a little for the wrapping. A kind the model is not sent counts nothing.
func EntryBytes(e Entry, maxOutput int) int {
	if !sent(e.Kind) {
		return 0
	}
	out := len(e.Output)
	if e.Kind == KindShell && maxOutput > 0 && !e.TUI {
		out = min(out, maxOutput)
	}
	n := len(e.Cmd) + len(e.Text) + out + 40
	for _, c := range e.ToolCalls {
		n += len(c.Args)
	}
	return n
}

// sent tells whether the model is sent entries of kind k, as
// agent.Messages builds the conversation. A clear is not, nor is a kind
// kept for the journal alone: neither weighs in the context.
func sent(k string) bool {
	switch k {
	case KindShell, KindUser, KindAssistant, KindToolResult, KindInstructions, KindFile, KindSkill, KindContext, KindSummary:
		return true
	}
	return false
}

func trimExt(name string) string { return name[:len(name)-len(filepath.Ext(name))] }

// Short formats a token count the short way: 950, 12k, 1.2M.
func Short(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1e6)
}
