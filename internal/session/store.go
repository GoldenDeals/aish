package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/inebotov/aish/internal/bashstate"
)

// A session is several files side by side: <id>.jsonl the journal,
// <id>.state what the shell and the proxy were like, <id>.name the name the
// user gave it, <id>.lock held by the aish that has the session open.

// Saved is what a session keeps besides its journal.
type Saved struct {
	// Shell is how the shell differs from the one aish started with.
	Shell  bashstate.State `json:"shell"`
	Model  string          `json:"model,omitempty"`
	Effort string          `json:"effort,omitempty"`
}

func statePath(dir, id string) string { return filepath.Join(dir, id+".state") }

// SaveState writes st whole or not at all: it holds environment variables,
// so it is private to the user like the journal.
func SaveState(dir, id string, st Saved) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := statePath(dir, id) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath(dir, id))
}

// LoadState returns the zero Saved for a session that has none.
func LoadState(dir, id string) (Saved, error) {
	var st Saved
	b, err := os.ReadFile(statePath(dir, id))
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("%s: %w", statePath(dir, id), err)
	}
	return st, nil
}

// Dir is where the session's files are.
func (s *Session) Dir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filepath.Dir(s.path)
}

// Lock marks the session open, so that no other aish opens it too. A new
// journal started by Clear takes the lock over.
func (s *Session) Lock() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock != nil {
		return nil
	}
	f, err := lock(filepath.Dir(s.path), s.ID)
	if err != nil {
		return err
	}
	s.lock = f
	return nil
}

func (s *Session) Unlock() {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock(s.lock)
	s.lock = nil
}

func lock(dir, id string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, id+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("session %s is open in another aish", id)
	}
	return f, nil
}

func unlock(f *os.File) {
	if f != nil {
		os.Remove(f.Name()) // while still holding it
		f.Close()
	}
}

// isOpen reports whether another aish holds the session.
func isOpen(dir, id string) bool {
	if _, err := os.Stat(filepath.Join(dir, id+".lock")); err != nil {
		return false
	}
	f, err := lock(dir, id)
	if err != nil {
		return true
	}
	unlock(f) // left behind by an aish that died
	return false
}

// Info describes a session for `aish resume`.
type Info struct {
	ID       string
	Name     string
	Modified time.Time
	Last     string // the last request
	Requests int
	Cwd      string
	Model    string
	Open     bool // in another aish, or in this one
}

// Title is the name, or the id for a session without one.
func (i Info) Title() string {
	if i.Name != "" {
		return i.Name
	}
	return i.ID
}

// List returns the sessions in dir, the most recent first.
func List(dir string) ([]Info, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	var list []Info
	for _, f := range files {
		id := trimExt(filepath.Base(f))
		info := Info{ID: id, Modified: modTime(f), Open: isOpen(dir, id)}
		if b, err := os.ReadFile(filepath.Join(dir, id+".name")); err == nil {
			info.Name = strings.TrimSpace(string(b))
		}
		if st, err := LoadState(dir, id); err == nil {
			info.Cwd, info.Model = st.Shell.Cwd, st.Model
		}
		info.Last, info.Requests = requests(f)
		list = append(list, info)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Modified.After(list[j].Modified) })
	return list, nil
}

// requests finds the last request and counts them in the end of a journal:
// the whole one may be large with command output.
func requests(path string) (last string, n int) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0
	}
	defer f.Close()
	const tail = 1 << 20
	if st, err := f.Stat(); err == nil && st.Size() > tail {
		f.Seek(-tail, io.SeekEnd)
	}
	b, _ := io.ReadAll(f)
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		if !bytes.Contains(line, []byte(`"kind":"user"`)) {
			continue
		}
		var e Entry
		if json.Unmarshal(line, &e) == nil && e.Kind == KindUser {
			last, n = e.Text, n+1
		}
	}
	return last, n
}

// Find picks a session by its id, its name, or the start of either.
func Find(list []Info, q string) (Info, error) {
	for _, i := range list {
		if i.ID == q || i.Name == q {
			return i, nil
		}
	}
	var found []Info
	lq := strings.ToLower(q)
	for _, i := range list {
		if strings.HasPrefix(i.ID, q) || i.Name != "" && strings.HasPrefix(strings.ToLower(i.Name), lq) {
			found = append(found, i)
		}
	}
	switch len(found) {
	case 0:
		return Info{}, fmt.Errorf("no session %q (aish resume lists them)", q)
	case 1:
		return found[0], nil
	}
	var names []string
	for _, i := range found {
		names = append(names, i.Title())
	}
	return Info{}, fmt.Errorf("%q matches %d sessions: %s", q, len(found), strings.Join(names, ", "))
}

// Rename names the session id; an empty name removes the name.
func Rename(dir, id, name string) error {
	name = strings.TrimSpace(name)
	path := filepath.Join(dir, id+".name")
	if name == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if strings.ContainsAny(name, "\n\r\t") || utf8.RuneCountInString(name) > 60 {
		return fmt.Errorf("a name is one line of at most 60 characters")
	}
	list, err := List(dir)
	if err != nil {
		return err
	}
	for _, i := range list {
		if i.ID != id && i.Name == name {
			return fmt.Errorf("session %s is already called %q", i.ID, name)
		}
	}
	return os.WriteFile(path, []byte(name+"\n"), 0o600)
}

// Load opens the session id in dir.
func Load(dir, id string) (*Session, error) {
	return Open(filepath.Join(dir, id+".jsonl"))
}
