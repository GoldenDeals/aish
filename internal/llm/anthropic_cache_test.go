package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/config"
)

// cacheMarks sends req through Complete and returns, for each message of the
// request body, its role and which of its blocks carry a cache breakpoint,
// along with the number of breakpoints in the whole request.
func cacheMarks(t *testing.T, req Request) (roles []string, marked [][]bool, total int) {
	t.Helper()
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, anthropicStream)
	}))
	defer srv.Close()

	p := newAnthropic(config.Config{Provider: "anthropic", Model: "m", APIKey: "k", BaseURL: srv.URL})
	if _, err := p.Complete(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Messages []struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	for _, m := range sent.Messages {
		roles = append(roles, m.Role)
		var marks []bool
		for _, b := range m.Content {
			marks = append(marks, b["cache_control"] != nil)
		}
		marked = append(marked, marks)
	}
	return roles, marked, strings.Count(string(body), `"cache_control"`)
}

// wantMarks checks that the user messages at the given indexes have a
// breakpoint on their last block and nowhere else, and that no other message
// has one.
func wantMarks(t *testing.T, roles []string, marked [][]bool, users ...int) {
	t.Helper()
	want := map[int]bool{}
	for _, i := range users {
		want[i] = true
	}
	for i, marks := range marked {
		for j, m := range marks {
			if exp := want[i] && j == len(marks)-1; m != exp {
				t.Errorf("message %d (%s), block %d of %d: breakpoint %v, want %v", i, roles[i], j, len(marks), m, exp)
			}
		}
	}
}

// A turn of 30 parallel calls adds 60 blocks, past the 20 a breakpoint looks
// back: the previous user message keeps the point the last turn wrote.
func TestAnthropicCacheManyCalls(t *testing.T) {
	var calls []ToolCall
	var results []ToolResult
	for i := range 30 {
		id := fmt.Sprintf("c%d", i)
		calls = append(calls, ToolCall{ID: id, Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)})
		results = append(results, ToolResult{CallID: id, Content: "a b"})
	}
	roles, marked, total := cacheMarks(t, Request{
		System: "system prompt",
		Tools:  []ToolDef{{Name: "bash", Description: "run", Schema: map[string]any{"properties": map[string]any{}}}},
		Messages: []Message{
			{Role: RoleUser, Text: "list files"},
			{Role: RoleAssistant, ToolCalls: calls},
			{Role: RoleUser, ToolResults: results},
		},
	})
	if len(marked) != 3 || len(marked[1]) != 30 || len(marked[2]) != 30 {
		t.Fatalf("unexpected request shape: %v", marked)
	}
	wantMarks(t, roles, marked, 0, 2)
	if total > 4 {
		t.Errorf("%d breakpoints in the request, the API allows 4", total)
	}
}

func TestAnthropicCacheOneMessage(t *testing.T) {
	roles, marked, total := cacheMarks(t, Request{
		System:   "system prompt",
		Tools:    []ToolDef{{Name: "bash", Description: "run", Schema: map[string]any{"properties": map[string]any{}}}},
		Messages: []Message{{Role: RoleUser, Text: "list files"}},
	})
	wantMarks(t, roles, marked, 0)
	if total != 3 {
		t.Errorf("%d breakpoints in the request, want 3", total)
	}
}

// Without tools and a system prompt only the last two user messages get one:
// the points do not spread further back into the history.
func TestAnthropicCacheNoToolsNoSystem(t *testing.T) {
	roles, marked, total := cacheMarks(t, Request{
		Messages: []Message{
			{Role: RoleUser, Text: "first"},
			{Role: RoleAssistant, Text: "one"},
			{Role: RoleUser, Text: "second"},
			{Role: RoleAssistant, Text: "two"},
			{Role: RoleUser, Text: "third"},
		},
	})
	wantMarks(t, roles, marked, 2, 4)
	if total != 2 {
		t.Errorf("%d breakpoints in the request, want 2", total)
	}
}
