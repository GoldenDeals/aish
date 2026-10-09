package agent

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/GoldenDeals/aish/internal/capture"
	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/tools"
)

// systemPrompt is assembled from Claude Code's system prompt (v2.1.286, as
// published in github.com/Piebald-AI/claude-code-system-prompts) adapted to
// aish's tools and live shell.
//
//go:embed system.md
var systemPrompt string

func system(env, extra string) string {
	var b strings.Builder
	b.WriteString(systemPrompt)
	b.WriteString("\n\n# Environment\n")
	b.WriteString(env)
	if extra != "" {
		b.WriteString("\n\n")
		b.WriteString(extra)
	}
	return b.String()
}

// environment describes the machine the agent runs on, the shell its bash
// tool runs in ("" for bash), the model that answers (provider "" being
// config.DefaultProvider) and user, the shell's $USER. It goes into the
// system prompt, the first thing the provider caches, so it holds nothing
// that changes from request to request of one shell: the date, the cwd and
// its git root are in the header of each request (Messages), where a cd or
// midnight leaves the history before them cached.
func environment(shell, model, provider, user string) string {
	var b strings.Builder
	host, _ := os.Hostname()
	osName := runtime.GOOS
	if out, err := exec.Command("uname", "-sr").Output(); err == nil {
		osName = strings.TrimSpace(string(out))
	}
	fmt.Fprintf(&b, "- OS: %s (%s), host %s, user %s\n", osName, runtime.GOARCH, host, user)
	if shell == "zsh" {
		b.WriteString("- Shell: zsh (interactive, the user's own ~/.zshrc). Your bash tool runs its commands in this zsh: write them for zsh, quote what zsh would glob, and keep to what zsh and bash read alike\n")
	} else {
		b.WriteString("- Shell: bash (interactive, the user's own ~/.bashrc)\n")
	}
	if provider == "" {
		provider = config.DefaultProvider
	}
	fmt.Fprintf(&b, "- Model: %s (%s)", model, provider)
	return b.String()
}

// gitRoot is the top of the git repository the request made in ex is in,
// as git finds it in the shell's environment, "" outside one. It is taken
// for the directory of the request, not for wherever its commands cd to.
func gitRoot(ctx context.Context, ex tools.Exec) string {
	if ex.Dir == "" {
		return "" // git would look at the proxy's own directory
	}
	cmd := exec.CommandContext(ctx, "git", "-C", ex.Dir, "rev-parse", "--show-toplevel")
	cmd.Env = ex.Env
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// requestCwd is the directory the last user request was made in, or cwd
// when there was none.
func requestCwd(entries []session.Entry, cwd string) string {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == session.KindUser && entries[i].Cwd != "" {
			return entries[i].Cwd
		}
	}
	return cwd
}

// Messages converts the session journal into a conversation. User commands
// and requests between two assistant turns become one user message. What
// the user did not type themselves (command output, files, instructions,
// skills, what hooks added, tool results) goes through mask; the journal
// keeps the original.
func Messages(entries []session.Entry, maxOutput int, mask *Masker) []llm.Message {
	entries = session.Current(entries)
	var out []llm.Message
	var user llm.Message
	var parts []string
	var inst, files, used, added []session.Entry
	flushInst := func() {
		if len(inst) > 0 {
			parts = append(parts, instructionsBlock(inst))
			inst = nil
		}
		if len(files) > 0 {
			parts = append(parts, filesBlock(files))
			files = nil
		}
		if len(used) > 0 {
			parts = append(parts, skillsBlock(used))
			used = nil
		}
		if len(added) > 0 {
			parts = append(parts, contextBlock(added))
			added = nil
		}
	}
	flush := func() {
		flushInst()
		user.Role = llm.RoleUser
		user.Text = strings.Join(parts, "\n\n")
		if user.Text != "" || len(user.ToolResults) > 0 {
			out = append(out, user)
		}
		user, parts = llm.Message{}, nil
	}
	for _, e := range entries {
		if e.Kind == session.KindUsage {
			continue // what a subagent's turn cost: no part of the context
		}
		if e.Kind != session.KindInstructions && e.Kind != session.KindFile && e.Kind != session.KindSkill && e.Kind != session.KindContext {
			flushInst()
		}
		switch e.Kind {
		case session.KindInstructions:
			e.Text = mask.Mask(e.Text)
			inst = append(inst, e)
		case session.KindFile:
			e.Text = mask.Mask(e.Text)
			files = append(files, e)
		case session.KindSkill:
			e.Text = mask.Mask(e.Text)
			used = append(used, e)
		case session.KindContext:
			e.Text = mask.Mask(e.Text)
			added = append(added, e)
		case session.KindSummary:
			parts = append(parts, summaryBlock(e))
		case session.KindShell:
			e.Output = mask.Mask(e.Output)
			parts = append(parts, shellBlock(e, maxOutput))
		case session.KindUser:
			head := e.Time.Format("2006-01-02 15:04") + ", cwd " + e.Cwd
			if e.Repo != "" {
				head += ", git root " + e.Repo
			}
			parts = append(parts, "["+head+"]\n"+e.Text)
		case session.KindToolResult:
			user.ToolResults = append(user.ToolResults, llm.ToolResult{
				CallID: e.ToolCallID, Name: e.ToolName, Content: mask.Mask(e.Output), IsError: e.IsError,
			})
		case session.KindAssistant:
			flush()
			m := llm.Message{Role: llm.RoleAssistant, Text: e.Text, Raw: e.Raw, Provider: e.Provider, Model: e.Model}
			for _, c := range e.ToolCalls {
				m.ToolCalls = append(m.ToolCalls, llm.ToolCall{ID: c.ID, Name: c.Name, Args: c.Args})
			}
			out = append(out, m)
		}
	}
	flush()
	return out
}

func shellBlock(e session.Entry, maxOutput int) string {
	out := e.Output
	if !e.TUI {
		out = capture.Truncate(out, maxOutput)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<shell exit=%d cwd=%q>\n$ %s\n", e.Exit, e.Cwd, e.Cmd)
	if out != "" {
		b.WriteString(out)
		if !strings.HasSuffix(out, "\n") {
			b.WriteByte('\n')
		}
	}
	b.WriteString("</shell>")
	return b.String()
}

// contextBlock is what user-prompt hooks added to the request after it.
func contextBlock(es []session.Entry) string {
	var b strings.Builder
	b.WriteString("<system-reminder>\n")
	for i, e := range es {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("Added by the user-prompt hook " + e.About + ":\n" + e.Text)
	}
	b.WriteString("\n</system-reminder>")
	return b.String()
}

// pending returns the tool calls of the last assistant turn that have no
// result yet.
func pending(entries []session.Entry) []session.ToolCall {
	last := -1
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == session.KindAssistant {
			last = i
			break
		}
	}
	if last < 0 {
		return nil
	}
	done := map[string]bool{}
	for _, e := range entries[last+1:] {
		if e.Kind == session.KindToolResult {
			done[e.ToolCallID] = true
		}
	}
	var out []session.ToolCall
	for _, c := range entries[last].ToolCalls {
		if !done[c.ID] {
			out = append(out, c)
		}
	}
	return out
}

// steps counts assistant turns since the last user request.
func steps(entries []session.Entry) int {
	n := 0
	for i := len(entries) - 1; i >= 0 && entries[i].Kind != session.KindUser; i-- {
		if entries[i].Kind == session.KindAssistant {
			n++
		}
	}
	return n
}

// finished reports whether the last turn is a final assistant answer.
func finished(entries []session.Entry) bool {
	if len(entries) == 0 {
		return false
	}
	e := entries[len(entries)-1]
	return e.Kind == session.KindAssistant && len(e.ToolCalls) == 0
}
