package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// A session has two names: the user's (<id>.name: `aish session rename`,
// `aish new NAME`) and the model's (<id>.title), given after its first
// request. `aish resume` lists the sessions the user named; with --all it
// lists every one, by the model's name when the user gave none.

// Name is the one the user gave the session, "" if none.
func (s *Session) Name() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.name
}

// SetName gives the session the user's name; "" removes it. A session on
// disk has it there at once, one without entries yet with its first.
func (s *Session) SetName(name string) error {
	name = strings.TrimSpace(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Dir(s.path)
	if err := CheckName(dir, s.ID, name); err != nil {
		return err
	}
	if s.saved {
		if err := writeName(dir, s.ID, name); err != nil {
			return err
		}
	}
	s.name = name
	return nil
}

// SetTitle gives the session the model's name for it, title. The session
// is on disk by then: the title comes after a request. One the shell has
// left since (Unlock) is locked for the write, and one another aish holds
// now, or that was removed, gets none.
func (s *Session) SetTitle(title string) error {
	title = strings.TrimSpace(title)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.saved {
		return errors.New("the session is not on disk")
	}
	dir := filepath.Dir(s.path)
	if s.lock == nil {
		l, err := lock(dir, s.ID)
		if err != nil {
			return err
		}
		defer unlock(l)
		if _, err := os.Stat(s.path); err != nil {
			return err
		}
	}
	return os.WriteFile(titlePath(dir, s.ID), []byte(title+"\n"), 0o600)
}

func titlePath(dir, id string) string { return filepath.Join(dir, id+".title") }

// writeName puts name in the session's .name file; "" removes the file.
func writeName(dir, id, name string) error {
	path := filepath.Join(dir, id+".name")
	if name = strings.TrimSpace(name); name == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(path, []byte(name+"\n"), 0o600)
}

// readName is the name in a .name or .title file, "" without one.
func readName(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
