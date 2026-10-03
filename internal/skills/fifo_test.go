package skills

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// mkfifo makes a FIFO nobody writes to or reads from.
func mkfifo(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Skip(err)
	}
}

// quick runs f and fails the test if it takes a second: a FIFO opened for
// reading waits in open(2) for a writer. The FIFO is then opened for
// reading and writing, which lets the reader stuck in open(2) go and see
// the end of the FIFO, so that the test fails rather than hangs.
func quick[T any](t *testing.T, fifo string, f func() T) T {
	t.Helper()
	done := make(chan T, 1)
	go func() { done <- f() }()
	select {
	case v := <-done:
		return v
	case <-time.After(time.Second):
		if w, err := os.OpenFile(fifo, os.O_RDWR, 0); err == nil {
			w.Close()
		}
		t.Fatal("waits for a writer of the FIFO")
	}
	panic("unreachable")
}

type result struct {
	skills   []Skill
	problems []Problem
}

// A SKILL.md that is a FIFO, or a link to one, is a problem, not a skill,
// and the skills beside it are found as they were.
func TestFindFIFO(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	skills := filepath.Join(home, "proj", ".claude", "skills")
	fifo := filepath.Join(skills, "pipe", "SKILL.md")
	mkfifo(t, fifo)
	link := filepath.Join(skills, "link", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(skills, "real", "SKILL.md"), skill("real", "a skill"))

	got := quick(t, fifo, func() result {
		s, p := Find(filepath.Join(home, "proj"))
		return result{s, p}
	})
	if len(got.skills) != 1 || got.skills[0].Name != "real" || !got.skills[0].Project {
		t.Errorf("skills %+v, want real only", got.skills)
	}
	want := map[string]bool{fifo: true, link: true}
	for _, p := range got.problems {
		if !want[p.Path] || p.Msg != "not a regular file" {
			t.Errorf("unexpected problem %s: %s", p.Path, p.Msg)
		}
		delete(want, p.Path)
	}
	if len(want) > 0 {
		t.Errorf("problems not reported: %v", want)
	}
}

// A SKILL.md replaced by a FIFO after the skill was found fails the call
// at once: Instructions reads it anew.
func TestInstructionsFIFO(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	file := filepath.Join(home, "proj", ".claude", "skills", "pipe", "SKILL.md")
	write(t, file, skill("pipe", "a skill"))
	found, _ := Find(filepath.Join(home, "proj"))
	if len(found) != 1 {
		t.Fatalf("found %+v", found)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	mkfifo(t, file)
	err := quick(t, file, func() error {
		_, err := found[0].Instructions("")
		return err
	})
	if err == nil || !strings.Contains(err.Error(), file+": not a regular file") {
		t.Errorf("Instructions: %v", err)
	}
}
