package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"

	"github.com/GoldenDeals/aish/internal/agent"
	"github.com/GoldenDeals/aish/internal/capture"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// tasksCmd shows the subagents the assistant runs in the background, whose
// work does not go to the screen: a line each, or the output of one so far.
func tasksCmd(args []string) int {
	var id string
	switch {
	case len(args) == 0:
	case len(args) == 2 && args[0] == "show":
		id = args[1]
	default:
		return fail(errors.New("usage: aish tasks [show ID]"))
	}
	client, err := rpc.FromEnv()
	if err != nil {
		return fail(err)
	}
	if id != "" {
		var t rpc.Task
		if err := client.Call(rpc.MethodTasks, rpc.TasksParams{ID: id}, &t); err != nil {
			return fail(err)
		}
		printTask(os.Stdout, t)
		return 0
	}
	var list []rpc.Task
	if err := client.Call(rpc.MethodTasks, nil, &list); err != nil {
		return fail(err)
	}
	width := 0
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		width = w
	}
	printTasks(os.Stdout, list, width)
	return 0
}

// printTasks lays the list out in columns: id, subagent with the
// description of its task, state and as much of the task's first line as
// width leaves (all of it when 0).
func printTasks(w io.Writer, list []rpc.Task, width int) {
	if len(list) == 0 {
		fmt.Fprintln(w, "\x1b[2m(no subagents in the background)\x1b[0m")
		return
	}
	idW, agentW, stateW := 0, 0, 0
	for _, t := range list {
		idW = max(idW, runewidth.StringWidth(t.ID))
		agentW = max(agentW, runewidth.StringWidth(agent.TaskTitle(t.Agent, t.Desc)))
		stateW = max(stateW, runewidth.StringWidth(t.State))
	}
	for _, t := range list {
		head := runewidth.FillRight(t.ID, idW) + "  " + runewidth.FillRight(agent.TaskTitle(t.Agent, t.Desc), agentW) + "  " +
			runewidth.FillRight(t.State, stateW) + "  "
		task, _, _ := strings.Cut(strings.TrimSpace(t.Prompt), "\n")
		task = strings.Join(strings.Fields(task), " ")
		if n := width - 1 - runewidth.StringWidth(head); width > 0 {
			task = runewidth.Truncate(task, max(n, 0), "…")
		}
		fmt.Fprintln(w, strings.TrimRight("\x1b[1m"+t.ID+"\x1b[0m"+head[len(t.ID):]+task, " "))
	}
}

// printTask prints one with its task, dim, and its output, as `aish
// expand` prints a fold.
func printTask(w io.Writer, t rpc.Task) {
	text := t.Output
	// The model's or the user's text, printed whole: no sequence of its
	// own reaches the terminal.
	if q := agent.QuoteTask(capture.Clean([]byte(t.Prompt))); q != "" {
		text = "\x1b[2m" + q + "\x1b[0m\n"
		if t.Output != "" {
			text += "\n" + t.Output
		}
	}
	printFold(w, rpc.Fold{Title: fmt.Sprintf("%s %s (%s)", t.ID, agent.TaskTitle(t.Agent, t.Desc), t.State), Text: text})
	if t.Output == "" {
		fmt.Fprintln(w, "\x1b[2m(no output)\x1b[0m")
	}
}
