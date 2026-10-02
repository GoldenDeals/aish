// Package session is the journal of one aish shell: commands with their
// output, user requests, assistant turns and tool results. Next to it are
// kept the shell's state, so that `aish resume` brings back the shell along
// with what the assistant knows, and the session's name.
package session

import (
	"bufio"
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
	// KindSummary replaces everything before it: `aish compact` asked the
	// model to sum the session up, and only the summary is sent from then on.
	KindSummary = "summary"
)

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
	TUI    bool   `json:"tui,omitempty"`

	// user, assistant
	Text string `json:"text,omitempty"`

	// assistant
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// Raw is the provider's own encoding of the assistant message (thinking
	// blocks etc.), replayed verbatim when the same provider is used.
	Raw      json.RawMessage `json:"raw,omitempty"`
	Provider string          `json:"provider,omitempty"`
	Model    string          `json:"model,omitempty"`
	// What the request for this turn cost: everything sent, cache included,
	// the part of it read from the cache, and the reply.
	InputTokens  int `json:"input_tokens,omitempty"`
	CachedTokens int `json:"cached_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`

	// instructions, file
	Path  string `json:"path,omitempty"`
	About string `json:"about,omitempty"`

	// tool_result
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`

	// tool_result, file (a mention that could not be read)
	IsError bool `json:"is_error,omitempty"`
}

// Session is safe for concurrent use. Every appended entry is persisted to a
// JSONL file immediately.
type Session struct {
	mu      sync.Mutex
	ID      string
	path    string
	entries []Entry
	lock    *os.File
}

func New(dir string) (*Session, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	id := freshID(dir, "")
	return &Session{ID: id, path: filepath.Join(dir, id+".jsonl")}, nil
}

// Latest opens the most recently modified session in dir that no other
// aish has open.
func Latest(dir string) (*Session, error) {
	list, _ := List(dir)
	for _, i := range list {
		if !i.Open {
			return Load(dir, i.ID)
		}
	}
	return New(dir)
}

func Open(path string) (*Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := &Session{ID: trimExt(filepath.Base(path)), path: path}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			s.entries = append(s.entries, e)
		}
	}
	return s, sc.Err()
}

func (s *Session) Append(es ...Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, e := range es {
		if e.Time.IsZero() {
			e.Time = time.Now()
		}
		s.entries = append(s.entries, e)
		b, _ := json.Marshal(e)
		if _, err := f.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Entry(nil), s.entries...)
}

// Clear starts a fresh journal file, keeping the session object.
func (s *Session) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
	dir := filepath.Dir(s.path)
	s.ID = freshID(dir, s.ID)
	s.path = filepath.Join(dir, s.ID+".jsonl")
	if s.lock != nil {
		if f, err := lock(dir, s.ID); err == nil {
			unlock(s.lock)
			s.lock = f
		}
	}
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
// summary on.
func Current(es []Entry) []Entry {
	for i := len(es) - 1; i >= 0; i-- {
		if es[i].Kind == KindSummary {
			return es[i:]
		}
	}
	return es
}

// Tokens estimates the size of the context: what the last turn measured
// plus about four bytes a token for what came after it. Shell output counts
// up to maxOutput bytes, as much as the model is sent.
func Tokens(es []Entry, maxOutput int) int {
	es = Current(es)
	n, from := 0, 0
	for i := len(es) - 1; i >= 0; i-- {
		if es[i].InputTokens > 0 {
			n, from = es[i].InputTokens+es[i].OutputTokens, i+1
			break
		}
	}
	bytes := 0
	for _, e := range es[from:] {
		out := len(e.Output)
		if e.Kind == KindShell && maxOutput > 0 && !e.TUI {
			out = min(out, maxOutput)
		}
		bytes += len(e.Cmd) + len(e.Text) + out + 40
		for _, c := range e.ToolCalls {
			bytes += len(c.Args)
		}
	}
	return n + bytes/4
}

func modTime(p string) time.Time {
	st, err := os.Stat(p)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
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
