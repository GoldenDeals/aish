// Command aish runs your bash with an LLM agent built in.
//
//	aish [--resume]              start bash under aish (a new or the latest session)
//	aish resume [ID|NAME]        bring a session back, shell state included; pick one or rename
//	aish mcp                     the MCP servers and how they are doing
//	aish skills                  the skills that apply here and their problems
//	aish init bash               print the bash integration script
//	aish tool [NAME ARGS...]     list tools or run one
//	aish session show|clear      print or reset the current session
//	aish compact [FOCUS]         replace the session with a summary
//	aish status                  the context, the model and the settings
//	aish model [NAME] [EFFORT]   list the models, switch this shell's model or effort
//	aish expand                  print the outputs folded during the last request (Ctrl+O)
//	aish agent start -- TEXT     (internal) hand a request to the agent in the proxy
//	aish agent resume ID RC      (internal) continue it after a bash command
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/inebotov/aish/internal/config"
	"github.com/inebotov/aish/internal/mcp"
	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/session"
	"github.com/inebotov/aish/internal/shellinit"
	"github.com/inebotov/aish/internal/skills"
	"github.com/inebotov/aish/internal/tools"
)

const usage = `usage:
  aish [--resume]            start bash with aish (--resume continues the latest session)
  aish resume [ID|NAME]      continue a session: its history, env, functions, aliases
                             and cwd; without an argument choose one (r renames it)
  aish mcp                   show the MCP servers: state, tools, errors
  aish skills                show the skills of this directory and their problems
  aish init bash             print the bash integration script
  aish tool [NAME ARGS...]   list tools, or run one
  aish session show|clear    print or reset the current session
  aish compact [FOCUS]       replace the session with its summary (FOCUS: what to keep)
  aish status                show the context size, the model and the settings
  aish model [NAME] [EFFORT] list the models, or switch the model and/or the
                             effort (low … max, default) for this shell
  aish expand                print outputs folded during the last request (Ctrl+O)

In the shell: commands run as usual; text that is not a command goes to the
assistant. Prefix with ? to force the assistant, with ! to force bash.
The subcommands from resume to expand also work without "aish" in front
(status, model high, compact), unless a command of that name is on PATH.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return shell(cfg, args)
	}
	switch args[0] {
	case "init":
		if len(args) < 2 || args[1] != "bash" {
			return fail(errors.New("usage: aish init bash"))
		}
		fmt.Print(shellinit.Bash)
		return 0
	case "agent":
		return agentCmd(args[1:])
	case "tool":
		return toolCmd(cfg, args[1:])
	case "session":
		return sessionCmd(args[1:])
	case "compact":
		return compactCmd(args[1:])
	case "status":
		return statusCmd(cfg)
	case "model":
		return modelCmd(cfg, args[1:])
	case "resume":
		return resumeCmd(cfg, args[1:])
	case "mcp":
		return mcpCmd(cfg, args[1:])
	case "skills":
		return skillsCmd(cfg, args[1:])
	case "expand":
		return expandCmd()
	case "help":
		fmt.Print(usage)
		return 0
	}
	fmt.Fprint(os.Stderr, usage)
	return 2
}

func shell(cfg config.Config, args []string) int {
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
	if err != nil {
		return fail(err)
	}
	// Latest starts a new session when there is nothing to continue.
	return startShell(cfg, sess, resume && sess.Len() > 0)
}

// shellConfig is cfg with the model and the effort this shell uses: `aish
// model` may have switched them.
func shellConfig(cfg config.Config, client *rpc.Client) config.Config {
	var info rpc.Info
	if client.Call(rpc.MethodInfo, nil, &info) == nil && info.Model != "" {
		cfg.Model, cfg.Effort = info.Model, info.Effort
	}
	return cfg
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
			problems = append(problems, t.Name+": skipped, another tool has this name")
		}
	}
	if warn != nil {
		for _, p := range problems {
			warn(p)
		}
	}
	return reg
}

func toolCmd(cfg config.Config, args []string) int {
	// The project's tools too, as the agent would have them here.
	cwd, _ := os.Getwd()
	cfg, _, err := config.Project(cfg, cwd)
	if err != nil {
		return fail(err)
	}
	var problems []string
	reg := loadTools(cfg, func(s string) { problems = append(problems, s) })
	if len(args) == 0 {
		w := 12
		for _, t := range reg.All() {
			w = max(w, len(t.Name))
		}
		for _, t := range reg.All() {
			fmt.Printf("%-*s %s\n", w, t.Name, firstSentence(t.Desc))
		}
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "\x1b[2mmcp: %s\x1b[0m\n", p)
		}
		return 0
	}
	t, ok := reg.Get(args[0])
	if !ok || t.Name == tools.Bash {
		return fail(fmt.Errorf("no tool %q", args[0]))
	}
	if len(args) > 1 && (args[1] == "-h" || args[1] == "--help") {
		fmt.Printf("usage: %s\n\n%s\n", t.Usage(), t.Desc)
		var opts []string
		for _, a := range t.Args {
			if a.Desc != "" {
				opts = append(opts, fmt.Sprintf("  %-16s %s", a.Name, a.Desc))
			}
		}
		if len(opts) > 0 {
			fmt.Printf("\n%s\n", strings.Join(opts, "\n"))
		}
		return 0
	}
	targs, err := t.ParseCLI(args[1:], os.Stdin)
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	out, err := t.Execute(ctx, targs, nil)
	fmt.Print(out)
	if out != "" && !strings.HasSuffix(out, "\n") {
		fmt.Println()
	}
	if err != nil {
		return fail(err)
	}
	return 0
}

func sessionCmd(args []string) int {
	client, err := rpc.FromEnv()
	if err != nil {
		return fail(err)
	}
	if len(args) != 1 {
		return fail(errors.New("usage: aish session show|clear"))
	}
	switch args[0] {
	case "clear":
		var info rpc.Info
		if err := client.Call(rpc.MethodClear, nil, &info); err != nil {
			return fail(err)
		}
		fmt.Println("new session", info.SessionID)
	case "show":
		es, err := client.History()
		if err != nil {
			return fail(err)
		}
		for _, e := range es {
			printEntry(e)
		}
	default:
		return fail(errors.New("usage: aish session show|clear"))
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
		f.Text = strings.ReplaceAll(f.Text, "\r\n", "\n") // captured from the PTY
		fmt.Printf("\x1b[0m\x1b[36m%s\x1b[0m\n", f.Title)
		fmt.Print(f.Text)
		if !strings.HasSuffix(f.Text, "\n") {
			fmt.Println()
		}
		fmt.Print("\x1b[0m")
	}
	return 0
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
			fmt.Printf("         \x1b[36m⚙ %s\x1b[0m %s\n", c.Name, c.Args)
		}
	case session.KindInstructions:
		fmt.Printf("\x1b[2m%s instructions %s, %d bytes\x1b[0m\n", e.Time.Format("15:04:05"), e.Path, len(e.Text))
	case session.KindSummary:
		fmt.Printf("\x1b[2m%s summary of what came before\x1b[0m\n%s\n", e.Time.Format("15:04:05"), e.Text)
	case session.KindToolResult:
		status := "ok"
		if e.IsError {
			status = "error"
		}
		fmt.Printf("         \x1b[2m→ %s %s, %d bytes\x1b[0m\n", e.ToolName, status, len(e.Output))
	}
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
