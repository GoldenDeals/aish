package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/inebotov/aish/internal/capture"
	"github.com/inebotov/aish/internal/hooks"
	"github.com/inebotov/aish/internal/policy"
	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/tools"
)

// What hooks answer is laid over the request here: the policy is the
// hard default, and a hook only adds to it. A hook that fails, hangs or
// answers nonsense is told about in a dim line and passed over, except a
// pre-tool hook exiting non-zero: that is a deny, the simplest guard.

// hookState is what the hooks of a request need between the agent's
// calls: the hooks found when it began, and the arguments pre-tool hooks
// gave the commands left for the shell, whose results come with Resume.
type hookState struct {
	set  *hooks.Set
	args map[string]map[string]any // by call id
}

// loadHooks finds the hooks of the request beginning: once, as its
// config is.
func (a *Agent) loadHooks() {
	set, problems := hooks.Find(a.Cfg.HooksDir)
	for _, p := range problems {
		fmt.Fprintf(a.UI, "%s[aish: hook %v]%s\n", dim, p, reset)
	}
	a.hooks = hookState{set: set}
}

// handed takes out the arguments pre-tool hooks gave the command of call
// id, nil when they gave none.
func (h *hookState) handed(id string) map[string]any {
	args := h.args[id]
	delete(h.args, id)
	return args
}

// hookFailed tells whether the hook gave no answer to go by, and says so
// on the terminal.
func (a *Agent) hookFailed(r hooks.Result) bool {
	switch {
	case r.Err != nil:
		fmt.Fprintf(a.UI, "%s[aish: hook %s: %v]%s\n", dim, r.Hook, r.Err, reset)
	case r.Exit != 0:
		msg := fmt.Sprintf("exit %d", r.Exit)
		if r.Stderr != "" {
			msg += ": " + r.Stderr
		}
		fmt.Fprintf(a.UI, "%s[aish: hook %s: %s]%s\n", dim, r.Hook, msg, reset)
	default:
		return false
	}
	return true
}

// userPrompt runs the user-prompt hooks on a request and returns it as
// the model is to get it, with what they add. False means one turned it
// down: it is not recorded, the model never sees it.
func (a *Agent) userPrompt(ctx context.Context, text, cwd string) (string, bool, error) {
	in := struct {
		Prompt  string `json:"prompt"`
		Cwd     string `json:"cwd"`
		Session string `json:"session"`
	}{text, cwd, a.Journal.ID()}
	var added []string
	for _, h := range a.hooks.set.For(hooks.UserPrompt) {
		r := h.Run(ctx, a.exec, in)
		if ctx.Err() != nil {
			return "", false, ctx.Err()
		}
		if a.hookFailed(r) {
			continue
		}
		if d := r.Reply.Deny; d != nil {
			msg := "denied by hook " + h.Name
			if *d != "" {
				msg += ": " + *d
			}
			fmt.Fprintf(a.UI, "%s  ✗ %s%s\n", red, msg, reset)
			return "", false, nil
		}
		if c := strings.TrimSpace(r.Reply.Context); c != "" {
			fmt.Fprintf(a.UI, "%s  (%s: %s)%s\n", dim, h, summary(c), reset)
			added = append(added, "Added by the user-prompt hook "+h.Name+":\n"+c)
		}
	}
	if len(added) == 0 {
		return text, true, nil
	}
	return text + "\n\n<system-reminder>\n" + strings.Join(added, "\n\n") + "\n</system-reminder>", true, nil
}

// verdict is what becomes of a tool call once the policy and the pre-tool
// hooks have had their say.
type verdict struct {
	policy.Decision
	by       string         // who decided: "policy" or "hook NAME"
	args     map[string]any // what the call runs with
	replaced bool           // a hook gave the arguments
}

// preTool runs the pre-tool hooks on call c of t, which the policy, given
// in, decided d on. A hook can only make the verdict stricter: its allow
// does not undo the policy's ask, and a deny of the policy runs no hooks.
// Arguments a hook replaces go to the next hook and to the policy again,
// and of its two verdicts the stricter stays: the policy has its say over
// what runs, as over what was asked.
func (a *Agent) preTool(ctx context.Context, t tools.Tool, c session.ToolCall, in policy.Input, d policy.Decision) (verdict, error) {
	v := verdict{Decision: d, by: "policy", args: in.Args}
	hs := a.hooks.set.For(hooks.PreTool)
	if d.Action == policy.Deny || len(hs) == 0 {
		return v, nil
	}
	type decision struct {
		Action string `json:"action"`
		Reason string `json:"reason,omitempty"`
	}
	type input struct {
		policy.Input
		Session string   `json:"session"`
		Policy  decision `json:"policy"`
	}
	cur := in
	var ask *verdict
	for _, h := range hs {
		r := h.Run(ctx, a.exec, input{cur, a.Journal.ID(), decision{d.Action, d.Reason}})
		if ctx.Err() != nil {
			return v, ctx.Err()
		}
		by := "hook " + h.Name
		if r.Err == nil && r.Exit != 0 {
			reason := r.Stderr
			if reason == "" {
				reason = fmt.Sprintf("exit %d", r.Exit)
			}
			return verdict{Decision: policy.Decision{Action: policy.Deny, Reason: reason}, by: by, args: cur.Args}, nil
		}
		if a.hookFailed(r) {
			continue
		}
		if r.Reply.Args != nil {
			cur = policy.NewInput(in.Tool, r.Reply.Args, in.Cwd)
			cur.Server, cur.Model = in.Server, in.Model
			v.replaced = true
			fmt.Fprintf(a.UI, "%s  (arguments replaced by %s)%s\n", dim, h, reset)
		}
		switch r.Reply.Action {
		case policy.Deny:
			return verdict{Decision: policy.Decision{Action: policy.Deny, Reason: r.Reply.Reason}, by: by, args: cur.Args}, nil
		case policy.Ask:
			if ask == nil {
				ask = &verdict{Decision: policy.Decision{Action: policy.Ask, Reason: r.Reply.Reason}, by: by}
			}
		}
	}
	v.args = cur.Args
	if v.replaced {
		again, err := a.Policy.Check(ctx, cur)
		if err != nil {
			return v, err
		}
		if strictness(again.Action) > strictness(v.Action) {
			v.Decision = again
		}
		if handsOff(t) {
			if a.hooks.args == nil {
				a.hooks.args = map[string]map[string]any{}
			}
			a.hooks.args[c.ID] = cur.Args
		}
	}
	if ask != nil && strictness(policy.Ask) > strictness(v.Action) {
		v.Decision, v.by = ask.Decision, ask.by
	}
	return v, nil
}

func strictness(action string) int {
	switch action {
	case policy.Deny:
		return 2
	case policy.Ask:
		return 1
	}
	return 0
}

// postTool runs the post-tool hooks on the result of call c, made with
// args (nil: the model's), and records the result as they leave it. The
// terminal shows what the tool printed; a line says when the model gets
// something else.
func (a *Agent) postTool(ctx context.Context, c session.ToolCall, args map[string]any, out string, isErr bool) error {
	hs := a.hooks.set.For(hooks.PostTool)
	if len(hs) == 0 {
		return a.append(toolResult(c, out, isErr))
	}
	if args == nil {
		args, _ = tools.Decode(c.Args)
	}
	orig, by := out, ""
	for _, h := range hs {
		in := struct {
			Tool    string         `json:"tool"`
			Args    map[string]any `json:"args"`
			Output  string         `json:"output"`
			IsError bool           `json:"is_error"`
			Cwd     string         `json:"cwd"`
			Session string         `json:"session"`
		}{c.Name, args, out, isErr, a.exec.Dir, a.Journal.ID()}
		r := h.Run(ctx, a.exec, in)
		if ctx.Err() != nil {
			break // the tool ran: its result is recorded all the same
		}
		if a.hookFailed(r) || r.Reply.Output == nil {
			continue
		}
		out, by = *r.Reply.Output, h.String()
	}
	if out != orig {
		// A runaway hook must not flood the context.
		out = capture.Truncate(out, a.Cfg.MaxOutputBytes*4)
		fmt.Fprintf(a.UI, "%s  (the model gets the result as %s left it)%s\n", dim, by, reset)
	}
	if err := a.append(toolResult(c, out, isErr)); err != nil {
		return err
	}
	return ctx.Err()
}

// stop runs the stop hooks once the request is over; they answer nothing.
func (a *Agent) stop(ctx context.Context) {
	hs := a.hooks.set.For(hooks.Stop)
	if len(hs) == 0 || len(a.entries) == 0 {
		return
	}
	in := struct {
		Text         string `json:"text"`
		Steps        int    `json:"steps"`
		InputTokens  int    `json:"input_tokens"`
		CachedTokens int    `json:"cached_tokens"`
		OutputTokens int    `json:"output_tokens"`
		Cwd          string `json:"cwd"`
		Session      string `json:"session"`
	}{Text: a.entries[len(a.entries)-1].Text, Steps: steps(a.entries), Cwd: a.exec.Dir, Session: a.Journal.ID()}
	for i := len(a.entries) - 1; i >= 0 && a.entries[i].Kind != session.KindUser; i-- {
		in.InputTokens += a.entries[i].InputTokens
		in.CachedTokens += a.entries[i].CachedTokens
		in.OutputTokens += a.entries[i].OutputTokens
	}
	for _, h := range hs {
		r := h.Run(ctx, a.exec, in)
		if ctx.Err() != nil {
			return
		}
		a.hookFailed(r)
	}
}
