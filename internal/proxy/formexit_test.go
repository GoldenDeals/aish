package proxy

import (
	"reflect"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/agent"
)

// oneQuestion is a form of a single question.
func oneQuestion() []agent.Question {
	return twoQuestions()[:1]
}

// What the user typed after the Enter that answered the form is the
// shell's, as it would be without the form; Ctrl+C before it too, in the
// order it came.
func TestFormKeyRest(t *testing.T) {
	cols := 80
	p, _, res, cancel := formProxyOf(t, &cols, oneQuestion())
	defer cancel()
	if got := p.key([]byte("1\rls")); string(got) != "ls" {
		t.Errorf("after the form: %q, want %q", got, "ls")
	}
	if p.form != nil {
		t.Error("the form stayed open")
	}
	if r := result(t, res); r.err != nil || !reflect.DeepEqual(r.ans, []agent.Answer{{Picked: []string{"Rewrite"}}}) {
		t.Errorf("answers %+v, %v", r.ans, r.err)
	}

	p, _, res, cancel = formProxyOf(t, &cols, oneQuestion())
	defer cancel()
	if got := p.key([]byte("2\x03" + keyEnterSeq + "echo \x03x\r")); string(got) != "\x03echo \x03x\r" {
		t.Errorf("with Ctrl+C: %q", got)
	}
	if r := result(t, res); r.err != nil || !reflect.DeepEqual(r.ans, []agent.Answer{{Picked: []string{"Patch"}}}) {
		t.Errorf("with Ctrl+C: answers %+v, %v", r.ans, r.err)
	}

	// Nothing after the Enter: nothing for the shell.
	p, _, res, cancel = formProxyOf(t, &cols, oneQuestion())
	defer cancel()
	if got := p.key([]byte(keyDownSeq + keyEnterSeq)); len(got) != 0 {
		t.Errorf("the form's own keys: %q", got)
	}
	result(t, res)
}

// Ctrl+O after the Enter opens the viewer, as it would with the form gone;
// what came between them is the shell's.
func TestFormKeyRestViewer(t *testing.T) {
	cols := 80
	p, _, res, cancel := formProxyOf(t, &cols, oneQuestion())
	defer cancel()
	p.mu.Lock()
	p.folds = []Fold{{Title: "❯ ls", Text: "a\r\nb\r\n"}}
	p.mu.Unlock()
	if got := p.key([]byte("1\rab\x0fq")); string(got) != "ab" {
		t.Errorf("before Ctrl+O: %q", got)
	}
	if p.form != nil || p.view == nil {
		t.Errorf("form %v, viewer %v", p.form, p.view)
	}
	if r := result(t, res); r.err != nil || len(r.ans) != 1 {
		t.Errorf("answers %+v, %v", r.ans, r.err)
	}
	if !strings.Contains(p.out.(*terminal).String(), "\x1b[?1049h") {
		t.Error("the viewer is not on the screen")
	}
}

// The shell gone, the proxy takes the form off the screen and shows the
// cursor again; the form ends unanswered.
func TestRestoreScreenForm(t *testing.T) {
	cols := 80
	p, out, res, cancel := formProxy(t, &cols)
	defer cancel()
	p.restoreScreen()
	if s := out.String(); !strings.HasSuffix(s, "\x1b[?25h") {
		t.Errorf("the cursor is hidden: %q", s)
	}
	if r := result(t, res); r.ans != nil || r.err != nil {
		t.Errorf("answers %+v, %v", r.ans, r.err)
	}
	if p.form != nil {
		t.Error("the form stayed open")
	}
}

// The viewer is closed too, and what the form drew under it gets to the
// screen: the form's erase counts on it.
func TestRestoreScreenViewer(t *testing.T) {
	cols := 80
	p, out, res, cancel := formProxy(t, &cols)
	defer cancel()
	p.mu.Lock()
	p.folds = []Fold{{Title: "❯ ls", Text: "a\r\nb\r\n"}}
	p.mu.Unlock()
	p.key([]byte{ctrlO})
	if p.view == nil {
		t.Fatal("no viewer")
	}
	p.resized() // the form is drawn anew, under the viewer
	before := len(out.String())
	p.restoreScreen()
	s := out.String()[before:]
	if !strings.HasPrefix(s, "\x1b[?1049l\x1b[?25l") || !strings.Contains(s, "Which approach?") || !strings.HasSuffix(s, "\x1b[?25h") {
		t.Errorf("closed with %q", s)
	}
	if p.view != nil || p.form != nil || p.held != nil {
		t.Errorf("viewer %v, form %v, held %q", p.view, p.form, p.held)
	}
	if r := result(t, res); r.ans != nil || r.err != nil {
		t.Errorf("answers %+v, %v", r.ans, r.err)
	}

	// The viewer alone, at the prompt.
	p = New(p.sess)
	term := &terminal{}
	p.out = term
	p.view = newViewer([]Fold{{Title: "❯ ls", Text: "a\r\n"}}, 80, 24)
	p.restoreScreen()
	if s := term.String(); s != "\x1b[?1049l\x1b[?25h" || p.view != nil {
		t.Errorf("closed with %q, viewer %v", s, p.view)
	}
}
