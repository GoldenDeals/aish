package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"mvdan.cc/sh/v3/syntax"

	"github.com/GoldenDeals/aish/internal/capture"
	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/llm"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/shells"
	"github.com/GoldenDeals/aish/internal/skills"
	"github.com/GoldenDeals/aish/internal/subagent"
	"github.com/GoldenDeals/aish/internal/tools"
)

// A subagent is a nested Agent the task tool runs within one call: a
// journal of its own in memory, the system prompt of its file, the host's
// tools as its file allows them. The live shell is the host's, busy with
// the request, so the subagent's bash commands run as processes of their
// own. Only its final answer goes back, as the result of the call or, for
// one in the background (bgtask.go), of task_wait or task_result; of the
// rest the session keeps what its turns cost (subusage.go), out of the
// context.

const (
	subName = "task"
	// maxParallel is how many subagents of one call run at once.
	maxParallel = 4
	// subCapture bounds the output of a subagent's command kept for the
	// model, as the proxy bounds the live shell's.
	subCapture = 64 << 10
)

// Panes is a UI that can show several live outputs at once, one per
// subagent. A UI without it falls back to the Live of the task call, which
// shows them one after another: a second Live of the UI at once would take
// the place of the first.
type Panes interface {
	// Pane is the live output of a subagent of the call.
	Pane(title string) Live
	// ClosePanes ends the panes of the call once all its subagents are
	// done. The Finish of the last one running would not do: those past
	// maxParallel start as the first ones end, and between them the panes
	// would close and open again.
	ClosePanes()
}

// AddSubagents registers the task tool for the subagents found in the
// working directory, and beside it the tools of those in the background
// (bgtask.go); with none it does nothing. The latter come without task
// too while subagents started elsewhere are in the background: their
// answers are still the model's to take after a cd.
func (a *Agent) AddSubagents(defs []subagent.Def) {
	a.subs = defs
	known := a.backgroundKnown()
	if len(defs) == 0 && !known {
		return
	}
	if a.Tools == nil {
		a.Tools = &tools.Registry{}
	}
	// A tool of the user's named task keeps the name: the subagents are
	// not offered then.
	if ours := len(defs) > 0 && a.Tools.Add(&taskTool{a: a}); ours || known {
		for _, name := range bgToolNames {
			a.Tools.Add(&bgTool{a: a, name: name})
		}
	}
}

// taskTool runs subagents, several at once. It is streaming: the call's
// own live output is where they show their work when the UI has no panes.
type taskTool struct{ a *Agent }

func (*taskTool) Name() string    { return subName }
func (*taskTool) Streaming() bool { return true }

func (*taskTool) Args() []tools.Arg {
	return []tools.Arg{
		{Name: "tasks", Type: "array", Required: true,
			Desc: `Tasks to run in parallel, as JSON: [{"agent": NAME, "prompt": TEXT}, …]`},
		{Name: "background", Type: "boolean", Flag: true, Desc: bgArgDesc},
	}
}

const bgArgDesc = "Return at once and let the subagents work in the background; " +
	"their answers come with task_wait or task_result"

func (t *taskTool) Desc() string {
	var b strings.Builder
	b.WriteString("Runs subagents: each works on its task alone and returns its final text answer, nothing else. " +
		"Several tasks in one call run in parallel.\n\n" +
		"Use it to hand off a self-contained piece of work, such as a wide search or a review, " +
		"that would otherwise fill this conversation with what it reads.\n\n" +
		"Usage:\n" +
		"- A subagent starts with a fresh context: it knows nothing of this conversation. " +
		"Its prompt must say everything it needs, and what to answer with.\n" +
		"- Independent tasks go into one call, so that they run at once.\n" +
		"- The answers come back to you, not to the user: tell the user what matters in them.\n" +
		"- With background, the call returns at once, a line \"started ID (NAME)\" for each subagent, and they work on " +
		"while you go on. Use it when you do not need the answers for your next step and the work takes minutes: " +
		"meanwhile do your part or answer the user. Only task_wait (which waits) and task_result (which does not) tell " +
		"whether they are done and give their answers, in a later request as well; task_cancel stops them. They work on " +
		"past this request, until aish clear or the end of aish, at most 8 unfinished at once.\n\n" +
		"Available subagents:\n")
	for _, d := range t.a.subs {
		fmt.Fprintf(&b, "- %s — %s\n", d.Name, d.Desc)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (t *taskTool) Schema() map[string]any {
	names := make([]string, len(t.a.subs))
	for i, d := range t.a.subs {
		names[i] = d.Name
	}
	task := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent":  map[string]any{"type": "string", "enum": names, "description": "The subagent to run"},
			"prompt": map[string]any{"type": "string", "description": "The task, with everything the subagent needs to know"},
		},
		"required": []string{"agent", "prompt"},
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tasks": map[string]any{
				"type": "array", "minItems": 1, "items": task,
				"description": "The tasks, one subagent each; they run in parallel",
			},
			"background": map[string]any{"type": "boolean", "description": bgArgDesc},
		},
		"required": []string{"tasks"},
	}
}

// Title names the subagents of the call.
func (t *taskTool) Title(args map[string]any) string {
	var names []string
	for _, v := range taskList(args) {
		if m, ok := v.(map[string]any); ok {
			if s, _ := m["agent"].(string); s != "" {
				names = append(names, s)
			}
		}
	}
	return strings.TrimSpace(subName + " " + strings.Join(names, ", "))
}

// taskList is the tasks argument; some models send the array as JSON text.
func taskList(args map[string]any) []any {
	v := args["tasks"]
	if s, ok := v.(string); ok {
		_ = json.Unmarshal([]byte(s), &v)
	}
	list, _ := v.([]any)
	return list
}

type subJob struct {
	def    subagent.Def
	prompt string
}

// jobs reads the tasks of a call. A mistake in any of them fails the call
// before a subagent starts: the model fixes it and calls again.
func (t *taskTool) jobs(args map[string]any) ([]subJob, error) {
	list := taskList(args)
	if len(list) == 0 {
		return nil, errors.New("tasks: at least one task is needed")
	}
	var out []subJob
	for i, v := range list {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tasks[%d]: an object with agent and prompt is needed", i)
		}
		name, _ := m["agent"].(string)
		k := slices.IndexFunc(t.a.subs, func(d subagent.Def) bool { return d.Name == name })
		if k < 0 {
			var known []string
			for _, d := range t.a.subs {
				known = append(known, d.Name)
			}
			return nil, fmt.Errorf("tasks[%d]: unknown subagent %q (there are %s)", i, name, strings.Join(known, ", "))
		}
		prompt, _ := m["prompt"].(string)
		if strings.TrimSpace(prompt) == "" {
			return nil, fmt.Errorf("tasks[%d]: empty prompt for %s", i, name)
		}
		out = append(out, subJob{t.a.subs[k], prompt})
	}
	return out, nil
}

// inBackground is the background argument; some models send it as text.
func inBackground(args map[string]any) bool {
	switch v := args["background"].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	}
	return false
}

func (t *taskTool) Execute(ctx context.Context, _ tools.Exec, args map[string]any, live io.Writer) (string, error) {
	jobs, err := t.jobs(args)
	if err != nil {
		return "", err
	}
	a := t.a
	runs := make([]*subRun, len(jobs))
	for i, j := range jobs {
		runs[i] = a.prepSub(j.def, j.prompt)
	}
	if inBackground(args) {
		for _, r := range runs {
			r.limit = givenLimit(ctx) // the call ends now, they work on
		}
		return a.background().start(runs, live)
	}
	var open func(title string) Live
	if p, ok := a.UI.(Panes); ok {
		open = p.Pane
		defer p.ClosePanes()
	} else {
		if live == nil {
			live = a.UI
		}
		open = (&relay{w: live, bol: true}).add
	}
	type result struct {
		reply string
		err   error
	}
	res := make([]result, len(jobs))
	slots := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	// Started in the order of the call, so that panes and sections come in
	// that order too.
	for i, j := range jobs {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		w := open(j.def.Name)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			reply, err := runSafe(ctx, runs[i], w)
			switch {
			case err == nil:
				w.Finish(0)
			case ctx.Err() != nil:
				w.Finish(130)
			default:
				fmt.Fprintf(w, "%s✗ %v%s\n", red, err, reset)
				w.Finish(1)
			}
			res[i] = result{reply, err}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	blocks := make([]string, len(jobs))
	for i, j := range jobs {
		status, text := outcome(res[i].reply, res[i].err)
		blocks[i] = block(j.def.Name, status, text)
	}
	return strings.Join(blocks, "\n\n"), nil
}

// outcome is the status of a subagent that answered reply or failed with
// err, and the text of its block.
func outcome(reply string, err error) (status, text string) {
	if err != nil {
		return "error", strings.TrimSpace(reply + "\n\n" + err.Error())
	}
	return "ok", reply
}

// block is a subagent's part of a result: its answer under a heading.
func block(title, status, text string) string {
	if text == "" {
		text = "(no reply)"
	}
	return fmt.Sprintf("## %s (%s)\n%s", title, status, text)
}

// runSafe is runSub in a goroutine of its own: a panic there would take
// the proxy, and the shell with it, down.
func runSafe(ctx context.Context, s *subRun, out Live) (reply string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("subagent %s: %v", s.def.Name, r)
		}
	}()
	return runSub(ctx, s, out)
}

// subNote opens the system prompt of a subagent: the common part, written
// for the agent of the live shell, would tell it wrong.
const subNote = "# Subagent\n" +
	"You are running as a subagent: another agent gave you a task, and your final text answer is all it gets back, " +
	"so make that answer complete and self-contained. Nobody can answer your questions: do the task, or say what stopped you. " +
	"What is said above about the live shell does not hold for you: your bash commands run in processes of their own " +
	"(see the bash tool), and nothing you do there stays in the user's shell."

// subRun is a subagent ready to run: all it needs of the host, taken by
// the request that calls task. One in the background outlives that
// request, and the next one's prepare sets the host's Cfg, Provider, Tools
// and Policy anew: the subagent's goroutine reads none of the host's fields.
type subRun struct {
	def    subagent.Def
	prompt string
	cfg    config.Config
	prov   llm.Provider
	err    error // making prov failed: the subagent's error, not the call's
	reg    *tools.Registry
	scope  *bashScope
	pol    *policy.Engine
	ex     tools.Exec
	yolo   func() bool // the host's Yolo: asked at each call, not taken now
	// journal is the host's, sess its ID as the call found it: what the
	// subagent's turns cost goes there while the shell is in that session.
	journal Journal
	sess    string
	// limit is how long one in the background may work, as the call of
	// task gave it; 0 is no limit.
	limit time.Duration
}

// prepSub takes what subagent d needs to work on prompt in the host's
// shell situation.
func (a *Agent) prepSub(d subagent.Def, prompt string) *subRun {
	s := &subRun{def: d, prompt: prompt, cfg: a.Cfg, prov: a.Provider, pol: a.Policy.Subagent(d.Name), ex: a.exec, yolo: a.Yolo}
	if a.Journal != nil {
		s.journal, s.sess = a.Journal, a.Journal.ID()
	}
	s.cfg.SystemPrompt = subNote + "\n\n" + d.Prompt
	if d.Model != "" && d.Model != s.cfg.Model {
		// The host's effort and window are its model's: another one may
		// not take that effort, and its window is not known here.
		s.cfg.Model, s.cfg.Effort, s.cfg.ContextWindow = d.Model, "", 0
		s.prov, s.err = llm.New(s.cfg)
	}
	s.reg, s.scope = defTools(a.Tools, d)
	return s
}

// runSub runs subagent s and returns its final answer. Its output goes to
// out.
func runSub(ctx context.Context, s *subRun, out Live) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	j := &memJournal{id: "sub:" + s.def.Name, spent: s.spent}
	sh := &subShell{}
	child := &Agent{Cfg: s.cfg, Provider: s.prov, Tools: s.reg, Policy: s.pol, Journal: j, Shell: sh, UI: subUI{out}, Yolo: s.yolo, name: s.def.Name}
	ex := s.ex
	if ex.Shell != "" && ex.Shell != "bash" {
		// Its commands run in a bash of their own (runCommand), not in the
		// user's shell, whose options and aliases are no bash's.
		ex.Shell, ex.Opts, ex.GlobalAliases = "bash", nil, nil
	}
	err := child.Start(ctx, s.prompt, ex)
	for err == nil {
		id, cmd, ok := sh.take()
		if !ok {
			break
		}
		var o rpc.Output
		// The scope of its file is a check too: aish yolo lifts it.
		if why := refused(s.scope, cmd, ex.Dir, ex.Env); why != "" && !child.yolo() {
			fmt.Fprintf(out, "%s  ✗ %s%s\n", red, why, reset)
			o = rpc.Output{Output: "not run: " + why, Exit: 126, Cwd: ex.Dir}
		} else {
			// The child's: its Cfg is the host's as the call found it.
			o = sh.run(ctx, id, ex.Dir, func(ctx context.Context) rpc.Output { return child.runCommand(ctx, cmd, ex, out) })
		}
		sh.done(id, o)
		err = child.Resume(ctx, id, o.Exit, ex)
	}
	reply := ""
	for i := len(j.es) - 1; i >= 0; i-- {
		if j.es[i].Kind == session.KindAssistant {
			reply = capture.Truncate(strings.TrimSpace(j.es[i].Text), s.cfg.MaxOutputBytes)
			break
		}
	}
	return reply, err
}

// runCommand runs a subagent's command in a bash of its own, in the
// shell's directory and environment, showing its output on out as it
// comes: the configured shell when it is a bash, as the policy reads the
// command as bash does. Its process group goes when ctx does, so that what
// it started does not outlive the request.
func (a *Agent) runCommand(ctx context.Context, cmd string, ex tools.Exec, out io.Writer) rpc.Output {
	shell := a.Cfg.Shell
	if shell == "" || shells.Kind(shell) != "bash" {
		shell = "bash"
	}
	buf := capture.NewBuffer(subCapture, subCapture)
	w := &lineEnd{w: io.MultiWriter(bufWriter{buf}, out), bol: true}
	c := exec.CommandContext(ctx, shell, "-c", cmd)
	c.Dir, c.Env = ex.Dir, ex.Env
	c.Stdout, c.Stderr = w, w
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	c.WaitDelay = time.Second
	err := c.Run()
	rc := 0
	var exit *exec.ExitError
	switch {
	case err == nil, errors.Is(err, exec.ErrWaitDelay):
		// Exited, with something it started in the background still
		// holding its output.
	case errors.As(err, &exit):
		rc = exit.ExitCode()
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			rc = 128 + int(ws.Signal())
		}
	default:
		rc = 127
		fmt.Fprintf(w, "%v\n", err)
	}
	why := ""
	if in := stoppedBy(ctx); in != nil {
		rc, why = in.Code, in.Why // its limit, or Esc: not the SIGKILL that ended it
	}
	if !w.bol {
		io.WriteString(out, "\n")
	}
	if rc != 0 {
		fmt.Fprintf(out, "%s  exit %d%s\n", dim, rc, reset)
	}
	text := capture.Clean(buf.Bytes())
	if buf.AltScreen() {
		text = "[full-screen interactive program; output not captured]"
	}
	return rpc.Output{Output: text, Exit: rc, Cwd: ex.Dir, Why: why}
}

// claudeTools maps the tool names of Claude Code's subagent files to
// aish's.
var claudeTools = map[string]string{
	"read": "read_file", "write": "write_file", "edit": "edit_file", "multiedit": "edit_file",
}

// searchTools are Claude Code's tools that only look. aish has no search
// tools of its own: they give bash, for the commands that search and read.
var searchTools = map[string]bool{"grep": true, "glob": true, "ls": true}

// readCommands are the commands Grep, Glob and LS give a subagent's bash.
var readCommands = []string{"cat", "find", "grep", "head", "ls", "rg", "tail", "wc"}

// skillTool is the type of a skill as a tool: the Skill entry gives those.
var skillTool = reflect.TypeOf(skills.Skill{}.Tool())

// bashScope is what the bash of a subagent may run, as its file says; nil
// for anything.
type bashScope struct {
	// patterns are of its Bash(...) entries, Claude Code's rules.
	patterns []string
	// readOnly is set by Grep, Glob and LS: the readCommands too, in a
	// line that writes nothing and runs nothing else (see onlyReads).
	readOnly bool
}

func (s *bashScope) String() string {
	pats := slices.Clone(s.patterns)
	if s.readOnly {
		for _, c := range readCommands {
			pats = append(pats, c+" *")
		}
	}
	return strings.Join(pats, ", ")
}

// subTools is the registry of a subagent whose file names the tools names,
// nil for all of them: the host's tools but task, as subagents run no
// subagents, and the dialogs, as nobody answers them. Scope is what its
// bash may run; nil for anything.
//
// Names are Claude Code's or aish's, in any case. Bash gives bash whole,
// Bash(...) for the commands of its patterns (a comma between them), and
// Grep, Glob and LS for the commands that search and read. Skill gives the
// skills, mcp__SERVER__TOOL that tool of an MCP server, mcp__SERVER all of
// the server's. A pattern of another tool (Read(src/**)) is not understood,
// and the entry gives nothing: a limit that cannot be kept does not turn
// into the whole tool.
func subTools(host *tools.Registry, names []string) (*tools.Registry, *bashScope) {
	allow := map[string]bool{}
	var mcps [][2]string // server and tool of the mcp__ entries
	allSkills := false
	whole := false // bash without a scope
	scope := &bashScope{}
	for _, n := range names {
		base, spec, scoped := strings.Cut(strings.TrimSpace(n), "(")
		var pats []string
		if scoped {
			for _, p := range strings.Split(strings.TrimSuffix(strings.TrimSpace(spec), ")"), ",") {
				if p = strings.TrimSpace(p); p != "" {
					pats = append(pats, p)
				}
			}
			scoped = len(pats) > 0 && !slices.Contains(pats, "*")
		}
		key := strings.ToLower(strings.TrimSpace(base))
		if m, ok := claudeTools[key]; ok {
			key = m
		}
		switch {
		case key == tools.Bash && scoped:
			allow[key] = true
			scope.patterns = append(scope.patterns, pats...)
		case scoped, key == subName:
		case key == tools.Bash:
			allow[key], whole = true, true
		case searchTools[key]:
			allow[tools.Bash], scope.readOnly = true, true
		case key == "skill":
			allSkills = true
		case strings.HasPrefix(key, "mcp__"):
			server, tool, _ := strings.Cut(key[len("mcp__"):], "__")
			mcps = append(mcps, [2]string{server, tool})
		default:
			allow[key] = true
		}
	}
	if whole || !allow[tools.Bash] {
		scope = nil
	}
	given := func(t tools.Tool) bool {
		return names == nil || allow[strings.ToLower(t.Name())] ||
			allSkills && reflect.TypeOf(t) == skillTool ||
			slices.ContainsFunc(mcps, func(m [2]string) bool { return mcpTool(t, m[0], m[1]) })
	}
	reg := &tools.Registry{}
	for _, t := range host.All() {
		if hostOnly(t) || tools.IsDialog(t) || !given(t) {
			continue
		}
		if _, ok := t.(tools.HandsOff); ok && t.Name() == tools.Bash {
			t = subBash{Tool: t, scope: scope}
		}
		reg.Add(t)
	}
	return reg, scope
}

// hostOnly tells whether t is task or a tool of the subagents in the
// background: subagents run no subagents.
func hostOnly(t tools.Tool) bool {
	switch t.(type) {
	case *taskTool, *bgTool:
		return true
	}
	return false
}

// mcpUnsafe is what Claude Code and internal/mcp make _ in the names of MCP
// servers and tools.
var mcpUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// mcpTool tells whether t is the tool Claude Code names mcp__SERVER__TOOL,
// or for an empty tool or * one of the server's. aish names it SERVER_TOOL,
// cut to 64 bytes.
func mcpTool(t tools.Tool, server, tool string) bool {
	s := tools.ServerOf(t)
	if s == "" || !strings.EqualFold(mcpUnsafe.ReplaceAllString(s, "_"), server) {
		return false
	}
	if tool == "" || tool == "*" {
		return true
	}
	name := server + "_" + tool
	return strings.EqualFold(t.Name(), name[:min(len(name), 64)])
}

// subBash is bash as a subagent has it: described for what it is there,
// a process per command, not the live shell.
type subBash struct {
	tools.Tool
	scope *bashScope
}

func (b subBash) Desc() string {
	d := "Executes a bash command and returns its output.\n\n" +
		"The command does not run in the user's live shell: each one runs in a new non-interactive bash process, " +
		"started in the user's working directory with their exported environment. Nothing persists between calls " +
		"(a cd, variables, functions), and the user's aliases and functions are not there.\n\n" +
		"Usage:\n" +
		"- Use absolute paths, and chain commands that depend on each other with '&&' in one call.\n" +
		"- Stdin is /dev/null: do not run interactive programs (editors, pagers, prompts); use non-interactive flags.\n" +
		"- Avoid cat, head, tail, sed, awk or echo to read, edit or write files when read_file, edit_file or write_file are available."
	if b.scope != nil {
		d += "\n\nOnly these commands may run, every command of the line matching one of them (* matches anything): " +
			b.scope.String() + ". Any other is refused, and so is a line that sets a variable but in lower case or of the " +
			"locale (LC_*, LANG, TZ), or does arithmetic on anything but numbers."
		if b.scope.readOnly {
			d += " " + strings.Join(readCommands, ", ") + " are there to search and read: a line with them may not " +
				"redirect output to a file or set variables; find runs without -delete, -exec, -execdir, -ok, -okdir, " +
				"-fls and -fprint*, rg without --pre and --hostname-bin, and the words of find and rg are written out: " +
				"quote patterns, with no $, *, ? or braces outside quotes."
		}
	}
	return d
}

func (b subBash) Command(args map[string]any) (string, bool) {
	return b.Tool.(tools.HandsOff).Command(args)
}

func (b subBash) Title(args map[string]any) string { return tools.Title(b.Tool, args) }

// refused tells why cmd, run in cwd with env, is not one a subagent with
// scope may run; "" when it is. Every simple command of the line, those in
// $(…) and bash -c included, must match a pattern, and code made at run
// time cannot be checked, so it does not run; nor does a line that sets a
// variable which may change what they run (setsVariable), or makes a name
// of a command run another program: Dynamic "prompt" and "rebind" are
// refused last, for a reason that names what the line sets. The variable
// is named before the commands the policy finds in its value too,
// GIT_SSH_COMMAND='sudo ls', and, when the line assigns it by its syntax,
// before a value made at run time, PAGER="$p"; env "$v"=x stays computed.
// set -k is named before the run time it leaves the lines after it in.
func refused(s *bashScope, cmd, cwd string, env []string) string {
	if s == nil {
		return ""
	}
	if cwd == "" {
		// Without it the policy's parser does not tell the files written.
		return "no working directory to check the command in"
	}
	in := policy.NewInput(tools.Bash, nil, cwd, env)
	in.HandOff(cmd)
	made := fmt.Sprintf("the command runs code made at run time (%s), which cannot be checked", strings.Join(in.Dynamic, ", "))
	keyword := slices.ContainsFunc(in.Commands, func(argv []string) bool { return argvSets(argv) == setK })
	switch {
	case in.ParseError != "":
		return "cannot parse the command: " + in.ParseError
	case slices.ContainsFunc(in.Dynamic, func(k string) bool { return k != "prompt" && k != "rebind" }) &&
		setsVariable(cmd, nil) == "" && !keyword:
		return made
	}
	// A line without commands may still write: > file.
	reads := len(in.Commands) == 0
	for _, argv := range in.Commands {
		line := strings.Join(argv, " ")
		switch {
		case slices.ContainsFunc(s.patterns, func(p string) bool { return matchCommand(p, line) }):
		case s.readOnly && slices.Contains(readCommands, argv[0]):
			if opt := unsafeOption(argv); opt != "" {
				return fmt.Sprintf("%s %s changes files or runs commands, and this subagent may only read with %s", argv[0], opt, argv[0])
			}
			reads = true
		default:
			if why := setsVariable(cmd, in.Commands); why != "" {
				return why
			}
			return fmt.Sprintf("%s is not among the commands this subagent may run: %s", argv[0], s)
		}
	}
	if reads {
		if len(in.Writes) > 0 {
			return fmt.Sprintf("writes to %s: the commands of this line may only read", in.Writes[0])
		}
		if why := onlyReads(cmd); why != "" {
			return why
		}
	}
	if why := setsVariable(cmd, in.Commands); why != "" || len(in.Dynamic) == 0 {
		return why
	}
	// PATH=. and PS1=… are told above by name; hash -p, enable here.
	return made
}

// unsafeOption is an option that makes a read command do more than read:
// find's that delete, run commands or write files, rg's that run a
// command. "" when argv has none.
func unsafeOption(argv []string) string {
	for _, a := range argv[1:] {
		switch argv[0] {
		case "find":
			switch {
			case a == "-delete", a == "-exec", a == "-execdir", a == "-ok", a == "-okdir", a == "-fls",
				strings.HasPrefix(a, "-fprint"):
				return a
			}
		case "rg":
			if name, _, _ := strings.Cut(a, "="); name == "--pre" || name == "--hostname-bin" {
				return a
			}
		}
	}
	return ""
}

// onlyReads tells what in the line cmd, past the options of its commands,
// the files it writes and the variables it sets (setsVariable), makes it
// do more than read: declare or export, a word of find or rg made at run
// time, which may be an option. "" when nothing.
func onlyReads(cmd string) string {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(cmd), "")
	if err != nil {
		return "cannot parse the command: " + err.Error()
	}
	why := ""
	syntax.Walk(f, func(n syntax.Node) bool {
		if why != "" {
			return false
		}
		switch n := n.(type) {
		case *syntax.DeclClause:
			why = fmt.Sprintf("%s sets variables, which may change what the commands of this line run", n.Variant.Value)
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				break
			}
			if name := unquoted(n.Args[0]); name == "find" || name == "rg" {
				for _, w := range n.Args[1:] {
					if !literal(w) {
						why = fmt.Sprintf("%s has a word made at run time, %s: what may come in as an option is not known; write it out", name, printed(w))
						break
					}
				}
			}
		}
		return true
	})
	return why
}

// harmlessVar tells whether setting name leaves the commands of a line as
// they are: programs read variables in upper case, and bash has no special
// one in lower case. Of those in upper case only the locale's are harmless;
// of those in lower case the proxy's are not, as git, curl and wget read
// them in lower case: https_proxy=… git push goes through any proxy.
func harmlessVar(name string) bool {
	switch {
	case proxyVars[name]:
		return false
	case strings.ToLower(name) == name, strings.HasPrefix(name, "LC_"):
		return true
	}
	return name == "LANG" || name == "LANGUAGE" || name == "TZ" || name == "NO_COLOR"
}

var proxyVars = map[string]bool{
	"all_proxy": true, "ftp_proxy": true, "http_proxy": true, "https_proxy": true, "no_proxy": true,
	"rsync_proxy": true, "socks_proxy": true,
}

// literal tells whether a word is the same text whatever the shell's
// state: quotes and plain characters, no expansion or glob.
func literal(w *syntax.Word) bool {
	for _, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			if strings.ContainsAny(p.Value, "*?[{") {
				return false
			}
		case *syntax.SglQuoted:
			if p.Dollar {
				return false
			}
		case *syntax.DblQuoted:
			if p.Dollar {
				return false
			}
			for _, q := range p.Parts {
				if _, ok := q.(*syntax.Lit); !ok {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

func printed(w *syntax.Word) string {
	var b strings.Builder
	syntax.NewPrinter().Print(&b, w)
	return b.String()
}

// unquoted is a word without its quotes and backslashes: of a literal
// word, the text the program gets, but for quotes and backslashes of its
// own, which no name of a program or a device has.
func unquoted(w *syntax.Word) string {
	return strings.NewReplacer(`'`, "", `"`, "", `\`, "").Replace(printed(w))
}

// matchCommand matches a command line against a pattern of Claude Code's
// Bash(...) rules: * is any run of characters, a trailing " *" takes the
// bare command too, and the older "git diff:*" means "git diff *".
func matchCommand(pat, line string) bool {
	pat = strings.TrimSpace(pat)
	if p, ok := strings.CutSuffix(pat, ":*"); ok {
		pat = p + " *"
	}
	if p, ok := strings.CutSuffix(pat, " *"); ok && line == p {
		return true
	}
	parts := strings.Split(pat, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	ok, _ := regexp.MatchString("(?s)^"+strings.Join(parts, ".*")+"$", line)
	return ok
}

// memJournal is a subagent's journal: in memory, gone with the call. Its
// turns are told to spent, which keeps what they cost.
type memJournal struct {
	id    string
	es    []session.Entry
	spent func(session.Entry)
}

func (j *memJournal) ID() string               { return j.id }
func (j *memJournal) Len() int                 { return len(j.es) }
func (j *memJournal) Entries() []session.Entry { return slices.Clone(j.es) }
func (j *memJournal) Append(es ...session.Entry) error {
	j.es = append(j.es, es...)
	if j.spent != nil {
		for _, e := range es {
			if e.Kind == session.KindAssistant {
				j.spent(e)
			}
		}
	}
	return nil
}

// subShell is the Shell of a subagent: it keeps the command handed off
// for runSub to run, and gives Resume the output runSub collected. The
// limit of the command stops it (timeout.go): mu is for that, the timer
// fires on a goroutine of its own.
type subShell struct {
	mu      sync.Mutex
	id, cmd string
	handed  bool
	out     map[string]rpc.Output
	stop    context.CancelCauseFunc // of the command running, see run
	early   *Interruption           // asked for before it ran
}

func (s *subShell) HandOff(id, cmd string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id, s.cmd, s.handed, s.early = id, cmd, true, nil
	return nil
}

func (s *subShell) take() (id, cmd string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ok, s.handed = s.handed, false
	return s.id, s.cmd, ok
}

func (s *subShell) done(id string, o rpc.Output) {
	if s.out == nil {
		s.out = map[string]rpc.Output{}
	}
	s.out[id] = o
}

func (s *subShell) Wait(_ context.Context, id string, _ time.Duration) (rpc.Output, error) {
	o, ok := s.out[id]
	if !ok {
		return rpc.Output{}, fmt.Errorf("no output of %s", id)
	}
	delete(s.out, id)
	return o, nil
}

// subUI is a subagent's terminal: its output, plain text, without a
// spinner. A call's line is closed already when a result is folded or a
// tool streams, so neither repeats it.
type subUI struct{ out Live }

func (u subUI) Write(b []byte) (int, error) { return u.out.Write(b) }
func (subUI) Size() (int, int)              { return 0, 0 }
func (subUI) CommandAt(int, bool, int)      {}

func (subUI) Ask(context.Context, string) (string, error) {
	return "", errors.New("a subagent cannot ask the user")
}

func (subUI) Form(context.Context, []Question) ([]Answer, error) {
	return nil, errors.New("a subagent cannot ask the user")
}

func (u subUI) Fold(_, text string) { fmt.Fprintf(u.out, "%s  %s%s\n", dim, summary(text), reset) }

func (u subUI) Live(string) Live { return nopFinish{u.out} }

// nopFinish is a tool's live output within the subagent's: that one ends
// with the subagent.
type nopFinish struct{ io.Writer }

func (nopFinish) Finish(int) {}

// relay shows the outputs of subagents running at once on one writer, one
// after another in the order of the call: the first one's as it comes,
// each of the others' kept until those before it are done.
type relay struct {
	mu   sync.Mutex
	w    io.Writer
	bol  bool // the last byte written ended a line
	secs []*section
	cur  int // the section on the writer now
}

// section is one subagent's output in a relay.
type section struct {
	r     *relay
	title string
	held  bytes.Buffer
	done  bool
}

func (r *relay) add(title string) Live {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := &section{r: r, title: title}
	r.secs = append(r.secs, s)
	if r.cur == len(r.secs)-1 {
		r.begin(s)
	}
	return s
}

// begin puts s on the writer, with what it held. Called under r.mu.
func (r *relay) begin(s *section) {
	if !r.bol {
		r.write([]byte("\n"))
	}
	r.write(fmt.Appendf(nil, "%s── %s%s\n", bold, s.title, reset))
	r.write(s.held.Bytes())
	s.held.Reset()
}

func (r *relay) write(b []byte) {
	if len(b) > 0 {
		r.w.Write(b)
		r.bol = b[len(b)-1] == '\n'
	}
}

func (s *section) Write(b []byte) (int, error) {
	r := s.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur < len(r.secs) && r.secs[r.cur] == s {
		r.write(b)
	} else {
		s.held.Write(b)
	}
	return len(b), nil
}

func (s *section) Finish(int) {
	r := s.r
	r.mu.Lock()
	defer r.mu.Unlock()
	s.done = true
	for r.cur < len(r.secs) && r.secs[r.cur].done {
		if r.cur++; r.cur < len(r.secs) {
			r.begin(r.secs[r.cur])
		}
	}
}

// lineEnd passes the output through and tells whether it ended a line.
type lineEnd struct {
	w   io.Writer
	bol bool
}

func (l *lineEnd) Write(b []byte) (int, error) {
	if len(b) > 0 {
		l.bol = b[len(b)-1] == '\n'
	}
	return l.w.Write(b)
}

type bufWriter struct{ b *capture.Buffer }

func (w bufWriter) Write(p []byte) (int, error) {
	w.b.Write(p)
	return len(p), nil
}
