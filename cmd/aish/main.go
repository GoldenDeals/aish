// Command aish runs your shell, bash or zsh, with an LLM agent built in.
//
//	aish [--resume]              start the shell under aish (a new or the latest session)
//	aish resume [--all] [ID|NAME]
//	                             bring a session back, shell state included; pick one or rename
//	aish mcp                     the MCP servers and how they are doing
//	aish skills                  the skills that apply here and their problems
//	aish hooks                   the hooks that run here, by event, and their problems
//	aish agents                  the subagents that apply here and their problems
//	aish tasks [show ID]         the subagents in the background, or one's output so far
//	aish policy [--agent NAME] [TOOL ARGS...]
//	                             check the policies, or ask them about one call (of subagent NAME)
//	aish policy --builtin        print the built-in policy
//	aish trust [--revoke|--list] let the .aish.toml here run the repository's hooks and tools
//	aish init bash|zsh           print the integration script of bash or zsh
//	aish completion bash|zsh     print the Tab completion of aish for bash or zsh
//	aish tool [NAME ARGS...]     list tools or run one
//	aish session show            print the current session
//	aish session rename [ID|NAME] NEWNAME
//	                             name this or another session, for aish resume
//	aish session rm ID|NAME...   remove sessions
//	aish session prune [--older AGE]
//	                             remove the sessions not used for AGE or sessions_ttl
//	aish clear                   start a new session; the current one stays saved
//	aish new [NAME]              start a new session with a name
//	aish compact [FOCUS]         replace the session with a summary
//	aish recap                   retell the whole session on the screen
//	aish status                  the model and the settings
//	aish context [--full]        how full the context is and with what; --full: the messages too
//	aish stats                   tokens, requests and sessions over 24 hours, 7, 30 and 90 days
//	aish model [PROFILE] [NAME] [EFFORT]
//	                             list the models, switch this shell's profile, model or effort
//	aish yolo [off]              turn the checks of the assistant's calls off till the shell exits, or on
//	aish apply-config            put the config files as they are now in force for this shell
//	aish expand                  print the outputs folded during the last request (Ctrl+O)
//	aish agent start -- TEXT     (internal) hand a request to the agent in the proxy
//	aish agent resume ID RC      (internal) continue it after a bash command
//	aish agent spawn NAME -- TEXT
//	                             (internal) start subagent NAME in the background, as &NAME TEXT at the prompt
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/mcp"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
	"github.com/GoldenDeals/aish/internal/shellinit"
	"github.com/GoldenDeals/aish/internal/skills"
	"github.com/GoldenDeals/aish/internal/tools"
)

var usage = `usage:
  aish [--resume]            start the shell with aish (--resume: the latest session)
  aish resume [--all] [ID|NAME]
                             continue a session: its history, env, functions, aliases
                             and cwd; without an argument choose one of those you
                             named (r renames it); --all: of all, by the names the
                             model gave them
  aish mcp                   show the MCP servers: state, tools, errors
  aish skills                show the skills of this directory and their problems
  aish hooks                 show the hooks of this directory in the order they run
  aish agents                show the subagents of this directory and their problems
  aish tasks [show ID]       show the subagents the assistant runs in the background,
                             or the output of one (bg1) so far
  aish policy [--agent NAME] [TOOL ARGS...]
                             check the policies, or ask them about one call (as
                             one of subagent NAME)
  aish policy --builtin      print the built-in policy, to copy into policy_dir
  aish trust [--revoke|--list]
                             let the .aish.toml here run hooks and tools from the
                             repository, as it is now; or take that back; or list
  aish init bash|zsh         print the integration script of bash or zsh
  aish completion bash|zsh   print Tab completion of aish for bash or zsh, for a
                             shell not under aish (one under it has it)
  aish tool [NAME ARGS...]   list tools, or run one
  aish session show          print the current session
  aish session rename [ID|NAME] NEWNAME
                             name this shell's session, or another one, for aish
                             resume to list it; "" takes the name away
  aish session rm ID|NAME... remove sessions, not the one of this shell
  aish session prune [--older AGE]
                             remove the sessions not used for AGE (30d, 12h),
                             sessions_ttl by default; open ones are kept
  aish clear                 start a new session; the current one stays on disk
  aish new [NAME]            start a new session, named NAME if given
  aish compact [FOCUS]       replace the session with its summary (FOCUS: what to keep)
  aish recap                 retell the whole session, on the screen only
  aish status                show the model and the settings
  aish context [--full]      show the context size and what fills it, by kind of
                             entry and by tool; --full also prints the messages
                             the model gets, as JSONL on stdout
  aish stats                 show the tokens, requests and sessions of the last 24
                             hours, 7, 30 and 90 days, subagents included, in all
                             and by model
  aish model [PROFILE] [NAME] [EFFORT]
                             list the profiles and the models, or switch the
                             profile of config.toml, the model and/or the
                             effort (low … max, default) for this shell
  aish yolo [off]            turn off the policies, the [policy] rules, the questions
                             and the limits of the subagents' bash for the assistant
                             till this shell exits (not the guard of aish trust,
                             not the hooks); off turns them back on
  aish apply-config          put config.toml, .aish.toml, the policies and mcp.yaml,
                             as they are now, in force for this shell: aish reads
                             them as it starts and with this command only
  aish expand                print outputs folded during the last request (Ctrl+O)

In the shell: commands run as usual; text that is not a command goes to the
assistant. Prefix with ? to force the assistant, with ! to force the shell.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fb := takeFallback()
	// Not with config.toml as read here: the proxy reads it and tells what
	// is wrong with it, and that nothing was applied.
	if len(args) > 0 && args[0] == "apply-config" {
		return applyConfigCmd(args[1:])
	}
	// Neither reads config.toml here: Tab and its scripts work with the
	// file broken on disk too.
	if len(args) > 0 && args[0] == "__complete" {
		return completeCmd(args[1:])
	}
	if len(args) > 0 && args[0] == "completion" {
		return completionCmd(args[1:])
	}
	// One reading of config.toml: the proxy keeps it, cfg is what it
	// selects here.
	conf := config.NewSnapshot()
	cfg, err := conf.LoadEnv(os.Getenv)
	if err != nil {
		// LoadProfile("") fails on all Load does but a profile the profile
		// key or $AISH_PROFILE names and config.toml has not, a table
		// renamed, say: on the top level every command still works,
		// `aish model root` too.
		top, topErr := conf.LoadProfile("")
		switch {
		case byProxy(args):
			// It goes by the config in force, or by none: the file waits
			// for aish apply-config, which tells what is wrong with it.
		case topErr != nil && startsProxy(args):
			return fb.fail("", err)
		case topErr != nil:
			return fail(err)
		case len(args) == 0 || args[0] != "agent":
			// Not on each request and after each of its commands: the
			// agent is the proxy's, which reads config.toml itself.
			fmt.Fprintf(os.Stderr, "\x1b[33maish: %v; on the top level of config.toml\x1b[0m\n", err)
		}
		cfg = top
	}
	if startsProxy(args) {
		return shell(conf, cfg, args, fb)
	}
	switch args[0] {
	case "init":
		switch {
		case len(args) == 2 && args[1] == "bash":
			fmt.Print(shellinit.Bash)
		case len(args) == 2 && args[1] == "zsh":
			fmt.Print(shellinit.Zsh)
		default:
			return fail(errors.New("usage: aish init bash|zsh"))
		}
		return 0
	case "agent":
		return agentCmd(args[1:])
	case "tool":
		return toolCmd(cfg, args[1:])
	case "session":
		return sessionCmd(cfg, args[1:])
	case "compact":
		return compactCmd(args[1:])
	case "recap":
		return recapCmd(args[1:])
	case "status":
		return statusCmd(cfg)
	case "context":
		return contextCmd(cfg, args[1:])
	case "stats":
		return statsCmd(cfg, args[1:])
	case "model":
		return modelCmd(cfg, args[1:])
	case "yolo":
		return yoloCmd(args[1:])
	case "resume":
		return resumeCmd(conf, cfg, args[1:])
	case "clear":
		return clearCmd(args[1:])
	case "new":
		return newCmd(args[1:])
	case "mcp":
		return mcpCmd(cfg, args[1:])
	case "skills":
		return skillsCmd(cfg, args[1:])
	case "agents":
		return agentsCmd(cfg, args[1:])
	case "tasks":
		return tasksCmd(args[1:])
	case "policy":
		return policyCmd(cfg, args[1:])
	case "trust":
		return trustCmd(cfg, args[1:])
	case "expand":
		return expandCmd()
	case "hooks":
		return hooksCmd(cfg, args[1:])
	case "help":
		fmt.Print(usage)
		return 0
	}
	fmt.Fprint(os.Stderr, usage)
	return 2
}

// shell starts the shell under aish; what keeps it from starting, fb tells
// and falls back on.
func shell(conf *config.Snapshot, cfg config.Config, args []string, fb fallback) int {
	resume := false
	for _, a := range args {
		switch a {
		case "--resume", "-r":
			resume = true
		case "--help", "-h":
			fmt.Print(usage)
			return 0
		default:
			fmt.Fprint(os.Stderr, usage)
			return 2
		}
	}
	if os.Getenv("AISH_SOCK") != "" {
		return fail(errors.New("already running inside aish"))
	}
	var sess *session.Session
	var err error
	if resume {
		sess, err = session.Latest(cfg.SessionsDir)
	} else {
		sess, err = session.New(cfg.SessionsDir)
	}
	failed := func(err error) int { return fb.fail(cfg.Shell, err) }
	if err != nil {
		return failed(err)
	}
	// Latest starts a new session when there is nothing to continue.
	return startShell(conf, cfg, sess, resume && sess.Len() > 0, failed)
}

func trimDashes(a []string) []string {
	if len(a) > 0 && a[0] == "--" {
		return a[1:]
	}
	return a
}

// loadTools returns the tools the user may run as commands: the built-in
// and external ones, the skills of cwd and, inside aish, the MCP tools the
// proxy provides, waiting for servers it has not started yet. Problems of
// the MCP tools go to warn; those of the skills are for `aish skills`. The
// agent's tools are the proxy's business.
func loadTools(cfg config.Config, warn func(string)) *tools.Registry {
	reg := tools.Load(cfg.ToolsDir)
	cwd, _ := os.Getwd()
	found, _ := skills.Find(cwd)
	for _, s := range found {
		reg.Add(s.Tool())
	}
	client, err := rpc.FromEnv()
	if err != nil {
		return reg
	}
	remote, problems := mcp.Remote(client, true)
	for _, t := range remote {
		if !reg.Add(t) {
			problems = append(problems, t.Name()+": skipped, another tool has this name")
		}
	}
	if warn != nil {
		for _, p := range problems {
			warn(p)
		}
	}
	return reg
}

// runnable is whether aish tool runs t: a command for the shell is typed as
// one, not through aish; questions for the user are the agent's to ask.
func runnable(t tools.Tool) bool {
	_, handsOff := t.(tools.HandsOff)
	return !handsOff && !tools.IsDialog(t)
}

// listTools prints the tools that can be run as `aish tool NAME`, one a line.
func listTools(w io.Writer, reg *tools.Registry) {
	var list []tools.Tool
	width := 12
	for _, t := range reg.All() {
		if runnable(t) {
			list = append(list, t)
			width = max(width, len(t.Name()))
		}
	}
	for _, t := range list {
		fmt.Fprintf(w, "%-*s %s\n", width, t.Name(), firstSentence(t.Desc()))
	}
}

func toolCmd(cfg config.Config, args []string) int {
	// The project's tools too, as the agent would have them here.
	cwd, _ := os.Getwd()
	a, err := inForce(cfg, cwd, nil, false)
	if err != nil {
		return fail(err)
	}
	cfg, project := a.cfg, a.project
	// Not on every run: the agent and the subagents call aish tool NAME
	// too, and the note would land in the output of each of their calls.
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, untrustedNote(cfg, project))
	}
	var problems []string
	reg := loadTools(cfg, func(s string) { problems = append(problems, s) })
	if len(args) == 0 {
		listTools(os.Stdout, reg)
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "\x1b[2mmcp: %s\x1b[0m\n", p)
		}
		return 0
	}
	t, ok := reg.Get(args[0])
	if !ok {
		fmt.Fprint(os.Stderr, untrustedNote(cfg, project))
	}
	if !ok || !runnable(t) {
		return fail(fmt.Errorf("no tool %q", args[0]))
	}
	if len(args) > 1 && (args[1] == "-h" || args[1] == "--help") {
		fmt.Printf("usage: %s\n\n%s\n", tools.Usage(t.Name(), t.Args()), t.Desc())
		var opts []string
		for _, a := range t.Args() {
			if a.Desc != "" {
				opts = append(opts, fmt.Sprintf("  %-16s %s", a.Name, a.Desc))
			}
		}
		if len(opts) > 0 {
			fmt.Printf("\n%s\n", strings.Join(opts, "\n"))
		}
		return 0
	}
	targs, err := tools.ParseCLI(t.Name(), t.Args(), args[1:], os.Stdin)
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	out, err := t.Execute(ctx, tools.Exec{}, targs, nil)
	fmt.Print(out)
	if out != "" && !strings.HasSuffix(out, "\n") {
		fmt.Println()
	}
	if err != nil {
		return fail(err)
	}
	return 0
}

func sessionCmd(cfg config.Config, args []string) int {
	const sessionUsage = "usage: aish session show | rename [ID|NAME] NEWNAME | rm ID|NAME... | prune [--older AGE]"
	if len(args) == 0 {
		return fail(errors.New(sessionUsage))
	}
	switch args[0] {
	case "show":
		if len(args) != 1 {
			return fail(errors.New(sessionUsage))
		}
		client, err := rpc.FromEnv()
		if err != nil {
			return fail(err)
		}
		es, err := client.History()
		if err != nil {
			return fail(err)
		}
		// The last reply that named its model: one the agent wrote itself
		// (Ctrl+C, out of steps) names none and changes nothing.
		var prev session.Entry
		for _, e := range es {
			if l := modelLine(prev, e); l != "" {
				fmt.Println(l)
			}
			if e.Kind == session.KindAssistant && e.Model != "" {
				prev = e
			}
			printEntry(e)
		}
	case "rename":
		return sessionRenameCmd(cfg, args[1:])
	case "rm":
		return sessionRmCmd(cfg, args[1:])
	case "prune":
		return sessionPruneCmd(cfg, args[1:])
	default:
		return fail(errors.New(sessionUsage))
	}
	return 0
}

func expandCmd() int {
	client, err := rpc.FromEnv()
	if err != nil {
		return fail(err)
	}
	var folds []rpc.Fold
	if err := client.Call(rpc.MethodFolds, nil, &folds); err != nil {
		return fail(err)
	}
	if len(folds) == 0 {
		fmt.Println("\x1b[2m(nothing folded)\x1b[0m")
		return 0
	}
	for _, f := range folds {
		printFold(os.Stdout, f)
	}
	return 0
}

// printFold prints one fold the way the Ctrl+O viewer shows it: the title
// line by line, the lines after the first indented, then the text.
func printFold(w io.Writer, f rpc.Fold) {
	fmt.Fprint(w, "\x1b[0m")
	for i, l := range strings.Split(f.Title, "\n") {
		if i > 0 {
			l = "  " + l
		}
		fmt.Fprintf(w, "\x1b[36m%s\x1b[0m\n", strings.ReplaceAll(l, "\t", "        "))
	}
	// The text is captured from the PTY; a command without output gets no
	// empty line under its title.
	if text := strings.ReplaceAll(f.Text, "\r\n", "\n"); text != "" {
		fmt.Fprint(w, text)
		if !strings.HasSuffix(text, "\n") {
			fmt.Fprintln(w)
		}
	}
	fmt.Fprint(w, "\x1b[0m")
}

func printEntry(e session.Entry) {
	switch e.Kind {
	case session.KindShell:
		note := ""
		if e.Output == session.NotRecorded {
			note = " " + session.NotRecorded
		}
		fmt.Printf("\x1b[2m%s\x1b[0m $ %s  \x1b[2m[exit %d]%s\x1b[0m\n", e.Time.Format("15:04:05"), e.Cmd, e.Exit, note)
	case session.KindUser:
		fmt.Printf("\x1b[2m%s\x1b[0m \x1b[1m? %s\x1b[0m\n", e.Time.Format("15:04:05"), e.Text)
	case session.KindAssistant:
		if e.Text != "" {
			fmt.Printf("\x1b[2m%s\x1b[0m %s\n", e.Time.Format("15:04:05"), e.Text)
		}
		for _, c := range e.ToolCalls {
			if qs, ok := questionsOf(c); ok {
				fmt.Printf("         \x1b[36m⚙ %s\x1b[0m\n", c.Name)
				for _, q := range qs {
					labels := make([]string, len(q.Options))
					for i, o := range q.Options {
						labels[i] = o.Label
					}
					several := ""
					if q.MultiSelect {
						several = " (several)"
					}
					fmt.Printf("           %s: %s \x1b[2m[%s]%s\x1b[0m\n", q.Name(), q.Question, strings.Join(labels, " | "), several)
				}
				continue
			}
			fmt.Printf("         \x1b[36m⚙ %s\x1b[0m %s\n", c.Name, c.Args)
		}
	case session.KindInstructions:
		fmt.Printf("\x1b[2m%s instructions %s, %d bytes\x1b[0m\n", e.Time.Format("15:04:05"), e.Path, len(e.Text))
	case session.KindContext:
		// Not the text: the user did not type it, and a hook's output can
		// be long.
		lines, s := strings.Count(e.Text, "\n")+1, "s"
		if lines == 1 {
			s = ""
		}
		fmt.Printf("\x1b[2m%s (context from hook %s, %d line%s)\x1b[0m\n", e.Time.Format("15:04:05"), e.About, lines, s)
	case session.KindSummary:
		fmt.Printf("\x1b[2m%s summary of what came before\x1b[0m\n%s\n", e.Time.Format("15:04:05"), e.Text)
	case session.KindClear:
		fmt.Printf("\x1b[2m%s screen cleared\x1b[0m\n", e.Time.Format("15:04:05"))
	case session.KindToolResult:
		status := "ok"
		if e.IsError {
			status = "error"
		}
		fmt.Printf("         \x1b[2m→ %s %s, %d bytes\x1b[0m\n", e.ToolName, status, len(e.Output))
		if isDialog(e.ToolName) {
			// The answers, short and the point of the call.
			for _, l := range strings.Split(e.Output, "\n") {
				fmt.Printf("           %s\n", l)
			}
		}
	}
}

// isDialog tells whether name is a tool whose calls are questions for the
// user, which the journal knows by name only.
func isDialog(name string) bool {
	t, ok := tools.Load("").Get(name)
	return ok && tools.IsDialog(t)
}

// questionsOf are the questions call c asked the user, if it is a dialog
// and they can be read.
func questionsOf(c session.ToolCall) ([]agent.Question, bool) {
	if !isDialog(c.Name) {
		return nil, false
	}
	args, err := tools.Decode(c.Args)
	if err != nil {
		return nil, false
	}
	qs, err := agent.ParseQuestions(args)
	return qs, err == nil
}

func firstSentence(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}

func fail(err error) int {
	fmt.Fprintf(os.Stderr, "aish: %v\n", err)
	return 1
}
