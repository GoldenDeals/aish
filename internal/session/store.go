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

	"github.com/GoldenDeals/aish/internal/shellstate"
)

// A session is several files side by side: <id>.jsonl the journal,
// <id>.state what the shell and the proxy were like, <id>.name the name the
// user gave it, <id>.title the one the model gave it after its first
// request, <id>.info what List found in the journal and state,
// <id>.lock held by the aish that has the session open.

// Saved is what a session keeps besides its journal.
type Saved struct {
	// Shell is how the shell differs from the one aish started with.
	Shell shellstate.State `json:"shell"`
	// Profile is the one of config.toml the model is of, "" for the top
	// level when TopLevel says so.
	Profile string `json:"profile,omitempty"`
	// TopLevel tells a Profile "" of the top level of config.toml from that
	// of a state saved before there were profiles, whose model is of the
	// profile the shell has.
	TopLevel bool   `json:"top_level,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
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

// Lock marks the session open, so that no other aish opens it too. One
// without entries is not on disk for another to open: its first entry
// locks it as it puts it there.
func (s *Session) Lock() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock != nil || !s.saved {
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
	ID string
	// Name is the one the user gave the session, AutoName the one the
	// model did: `aish resume` lists the sessions with a Name, `aish resume
	// --all` all of them.
	Name     string
	AutoName string
	Modified time.Time
	Last     string // the last request
	Requests int
	Cwd      string
	// Profile, TopLevel and Model are as the session's Saved has them.
	Profile  string
	TopLevel bool
	Model    string
	Open     bool // in another aish, or in this one
}

// Title is the user's name, else the model's, else the id.
func (i Info) Title() string {
	switch {
	case i.Name != "":
		return i.Name
	case i.AutoName != "":
		return i.AutoName
	}
	return i.ID
}

// Named are the sessions of list the user named, for `aish resume` without
// --all.
func Named(list []Info) []Info {
	var named []Info
	for _, i := range list {
		if i.Name != "" {
			named = append(named, i)
		}
	}
	return named
}

// List returns the sessions in dir, the most recent first, but bare ones
// the user did not name: there is nothing in them to resume.
func List(dir string) ([]Info, error) { return listing(dir, false) }

// listing is List, with the bare sessions too if all.
func listing(dir string, all bool) ([]Info, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	var list []Info
	for _, f := range files {
		id := trimExt(filepath.Base(f))
		if CheckID(id) != nil {
			continue // Load would refuse it
		}
		info := Info{ID: id, Open: isOpen(dir, id)}
		info.Name = readName(filepath.Join(dir, id+".name"))
		info.AutoName = readName(titlePath(dir, id))
		if bare := summarize(dir, id, &info); bare && info.Name == "" && !all {
			continue
		}
		list = append(list, info)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Modified.After(list[j].Modified) })
	return list, nil
}

// requests finds the last request and counts them in the end of a journal:
// the whole one may be large with command output. err tells that what it
// found is not of the journal as it was when it was opened.
func requests(path string) (last string, n int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	const tail = 1 << 20
	from := max(0, st.Size()-tail)
	b := make([]byte, st.Size()-from)
	read, err := f.ReadAt(b, from)
	if read == len(b) {
		err = nil
	} else if err == nil || err == io.EOF {
		err = fmt.Errorf("%s: cut short while read", path)
	}
	for b = b[:read]; len(b) > 0; {
		var line []byte
		line, b, _ = bytes.Cut(b, []byte{'\n'})
		// An entry aish wrote starts with its kind, and the rest of one
		// that is not a request need not be searched: most of it is output.
		if bytes.HasPrefix(line, []byte(`{"kind":"`)) && !bytes.HasPrefix(line, []byte(`{"kind":"user"`)) ||
			!bytes.Contains(line, []byte(`"kind":"user"`)) {
			continue
		}
		var e Entry
		if json.Unmarshal(line, &e) == nil && e.Kind == KindUser {
			last, n = e.Text, n+1
		}
	}
	return last, n, err
}

// Find picks a session by its id, the user's name for it, or the start of
// either.
func Find(list []Info, q string) (Info, error) { return find(list, q, false) }

// FindAll is Find that takes the model's names too, which `aish resume
// --all` shows: they need not differ, and one several sessions have is
// no answer.
func FindAll(list []Info, q string) (Info, error) { return find(list, q, true) }

func find(list []Info, q string, auto bool) (Info, error) {
	for _, i := range list {
		if i.ID == q || i.Name == q {
			return i, nil
		}
	}
	// The model's name stands for a session the user did not name, as
	// Title has it.
	autoName := func(i Info) string {
		if auto && i.Name == "" {
			return i.AutoName
		}
		return ""
	}
	var found []Info
	for _, i := range list {
		if n := autoName(i); n != "" && n == q {
			found = append(found, i)
		}
	}
	lq := strings.ToLower(q)
	prefix := func(name string) bool { return name != "" && strings.HasPrefix(strings.ToLower(name), lq) }
	if len(found) == 0 {
		for _, i := range list {
			if strings.HasPrefix(i.ID, q) || prefix(i.Name) || prefix(autoName(i)) {
				found = append(found, i)
			}
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

// Rename gives session id of dir the user's name; an empty name removes
// it. The session is locked meanwhile, so one that an aish holds is
// refused: that aish names it (SetName), as it may keep the name for a
// journal not on disk yet.
func Rename(dir, id, name string) error {
	if err := CheckID(id); err != nil {
		return err
	}
	l, err := lock(dir, id)
	if err != nil {
		return err
	}
	defer unlock(l)
	// Under the lock: Remove takes it to delete the files.
	if _, err := os.Stat(filepath.Join(dir, id+".jsonl")); err != nil {
		return fmt.Errorf("no session %s", id)
	}
	if err := CheckName(dir, id, name); err != nil {
		return err
	}
	return writeName(dir, id, name)
}

// CheckName tells whether Rename would give session id the name: one line
// of at most 60 characters that no other session in dir has. An empty name
// is no name and always fits.
func CheckName(dir, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if strings.ContainsAny(name, "\n\r\t") || utf8.RuneCountInString(name) > 60 {
		return fmt.Errorf("a name is one line of at most 60 characters")
	}
	// The names alone, not List: a proxy checks under its lock, and List
	// reads every journal.
	files, err := filepath.Glob(filepath.Join(dir, "*.name"))
	if err != nil {
		return err
	}
	for _, f := range files {
		other := trimExt(filepath.Base(f))
		if other == id || CheckID(other) != nil || readName(f) != name {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, other+".jsonl")); err == nil {
			return fmt.Errorf("session %s is already called %q", other, name)
		}
	}
	return nil
}

// Load opens the session id in dir.
func Load(dir, id string) (*Session, error) {
	if err := CheckID(id); err != nil {
		return nil, err
	}
	return Open(filepath.Join(dir, id+".jsonl"))
}
