package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/inebotov/aish/internal/session"
)

// A call of a dialog tool (ask_user) is not run: the agent reads the
// questions from it, the UI asks them on the terminal, and the answers go
// back to the model as the call's result, a line per question.

// Question is one question of the form, as the model asks it.
type Question struct {
	Question string `json:"question"`
	// Header is a short label: the title of the question and the name of
	// its answer.
	Header      string   `json:"header"`
	Options     []Option `json:"options"`
	MultiSelect bool     `json:"multi_select"`
}

type Option struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Answer is what the user chose for a question: the labels of the options
// picked, in their order, and the text typed under "Other", if any.
type Answer struct {
	Picked []string
	Other  string
}

// String is the answer as the model and the summary on the screen get it.
func (a Answer) String() string {
	parts := a.Picked
	if a.Other != "" {
		parts = append(parts[:len(parts):len(parts)], a.Other)
	}
	return strings.Join(parts, ", ")
}

// Name is how the question is named in the summary of the answers.
func (q Question) Name() string {
	if h := strings.TrimSpace(q.Header); h != "" {
		return h
	}
	return strings.TrimSpace(q.Question)
}

// The limits of the schema: more questions or options would not fit a
// form one can take in at a glance.
const (
	maxQuestions = 4
	minOptions   = 2
	maxOptions   = 4
)

// ParseQuestions reads the questions of an ask_user call. A mistake is told
// to the model, which may ask again.
func ParseQuestions(args map[string]any) ([]Question, error) {
	v := args["questions"]
	if s, ok := v.(string); ok {
		// Some models send the array as JSON text.
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return nil, fmt.Errorf("questions: %w", err)
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("questions: %w", err)
	}
	var qs []Question
	if err := json.Unmarshal(b, &qs); err != nil {
		return nil, fmt.Errorf("questions: %w", err)
	}
	if len(qs) == 0 || len(qs) > maxQuestions {
		return nil, fmt.Errorf("questions: 1 to %d are needed, got %d", maxQuestions, len(qs))
	}
	for i, q := range qs {
		if strings.TrimSpace(q.Question) == "" {
			return nil, fmt.Errorf("question %d: no text", i+1)
		}
		if n := len(q.Options); n < minOptions || n > maxOptions {
			return nil, fmt.Errorf("question %d: %d to %d options are needed, got %d", i+1, minOptions, maxOptions, n)
		}
		for j, o := range q.Options {
			if strings.TrimSpace(o.Label) == "" {
				return nil, fmt.Errorf("question %d, option %d: no label", i+1, j+1)
			}
		}
	}
	return qs, nil
}

// answerText is the result of the call: "header: answer" per question.
func answerText(qs []Question, ans []Answer) string {
	lines := make([]string, len(qs))
	for i, q := range qs {
		lines[i] = q.Name() + ": " + ans[i].String()
	}
	return strings.Join(lines, "\n")
}

// cancelled is the result when the user pressed Esc: an error, so that the
// model ends its turn rather than asks again.
const cancelled = "the user cancelled: stop and wait for their next request, do not ask again"

// dialog asks the questions of call c, made with args, and records the
// answers as its result. Ctrl+C leaves the call pending, as with any tool.
func (a *Agent) dialog(ctx context.Context, c session.ToolCall, args map[string]any) error {
	qs, err := ParseQuestions(args)
	if err != nil {
		fmt.Fprintf(a.UI, "%s  ✗ %v%s\n", red, err, reset)
		return a.postTool(ctx, c, args, err.Error(), true)
	}
	ans, err := a.UI.Form(ctx, qs)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	switch {
	case err != nil:
		msg := "cannot ask the user: " + err.Error()
		fmt.Fprintf(a.UI, "%s  ✗ %s%s\n", red, msg, reset)
		return a.postTool(ctx, c, args, msg, true)
	case ans == nil:
		fmt.Fprintf(a.UI, "%s  ✗ cancelled%s\n", red, reset)
		return a.postTool(ctx, c, args, cancelled, true)
	}
	return a.postTool(ctx, c, args, answerText(qs, ans), false)
}
