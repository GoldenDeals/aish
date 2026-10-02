package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inebotov/aish/internal/llm"
	"github.com/inebotov/aish/internal/session"
)

func TestMessages(t *testing.T) {
	call := session.ToolCall{ID: "t1", Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)}
	es := []session.Entry{
		{Kind: session.KindShell, Cmd: "make", Output: "error: boom", Exit: 2, Cwd: "/p"},
		{Kind: session.KindUser, Text: "почему упало?", Cwd: "/p"},
		{Kind: session.KindAssistant, Text: "посмотрю", ToolCalls: []session.ToolCall{call}},
		{Kind: session.KindToolResult, ToolCallID: "t1", ToolName: "bash", Output: "Makefile"},
		{Kind: session.KindAssistant, Text: "вот почему"},
		{Kind: session.KindShell, Cmd: "true", Cwd: "/p"},
		{Kind: session.KindUser, Text: "спасибо", Cwd: "/p"},
	}
	ms := Messages(es, 1000)
	roles := []string{}
	for _, m := range ms {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "user,assistant,user,assistant,user" {
		t.Fatalf("roles %v", roles)
	}
	if !strings.Contains(ms[0].Text, "$ make\nerror: boom") || !strings.Contains(ms[0].Text, "exit=2") || !strings.HasSuffix(ms[0].Text, "почему упало?") {
		t.Fatalf("first message %q", ms[0].Text)
	}
	if len(ms[2].ToolResults) != 1 || ms[2].ToolResults[0].CallID != "t1" || ms[2].Text != "" {
		t.Fatalf("tool result message %+v", ms[2])
	}
	if !strings.Contains(ms[4].Text, "$ true") || !strings.Contains(ms[4].Text, "спасибо") {
		t.Fatalf("last message %q", ms[4].Text)
	}
	_ = llm.RoleUser
}

func TestPendingAndSteps(t *testing.T) {
	es := []session.Entry{
		{Kind: session.KindUser, Text: "x"},
		{Kind: session.KindAssistant, ToolCalls: []session.ToolCall{{ID: "a"}, {ID: "b"}}},
		{Kind: session.KindToolResult, ToolCallID: "a"},
	}
	p := pending(es)
	if len(p) != 1 || p[0].ID != "b" {
		t.Fatalf("pending %v", p)
	}
	if steps(es) != 1 || finished(es) {
		t.Fatal("steps/finished")
	}
	es = append(es, session.Entry{Kind: session.KindToolResult, ToolCallID: "b"}, session.Entry{Kind: session.KindAssistant, Text: "done"})
	if len(pending(es)) != 0 || !finished(es) || steps(es) != 2 {
		t.Fatal("after answer")
	}
}

func TestMessagesFromSummary(t *testing.T) {
	es := []session.Entry{
		{Kind: session.KindUser, Text: "old request"},
		{Kind: session.KindAssistant, Text: "old answer"},
		{Kind: session.KindSummary, Text: "we did things"},
		{Kind: session.KindShell, Cmd: "ls"},
		{Kind: session.KindUser, Text: "next"},
	}
	ms := Messages(es, 1000)
	if len(ms) != 1 || ms[0].Role != llm.RoleUser {
		t.Fatalf("messages %+v", ms)
	}
	txt := ms[0].Text
	if strings.Contains(txt, "old") || !strings.HasPrefix(txt, "<summary>") || !strings.Contains(txt, "we did things") || !strings.Contains(txt, "$ ls") {
		t.Errorf("text %q", txt)
	}
}
