package agent

import (
	"fmt"
	"slices"
	"strings"

	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/subagent"
	"github.com/GoldenDeals/aish/internal/tools"
)

// The user starts a subagent in the background himself: `&NAME text` at the
// prompt. It is one of the set task starts with background (bgtask.go), in
// all but whose it is: the text is the user's, no turn of the model goes
// before it, and its answer is his alone. The journal gets only what its
// turns cost (subusage.go); the model's tools do not see it; `aish tasks
// show` gives its answer, and the first prompt after it ended tells of it
// (Ended), as it would of any task the user leaves in the background.

// Spawn starts subagent name on prompt in the background for the user. It
// is ready as a subagent of task: with the config, the provider, the tools
// and the policy the request's prepare left, and ex, the shell's situation.
// So it is in the background as any other: its bash a process of its own,
// ask a deny, the guard over it, aish yolo as the host has it; it lives till
// clear, resume or the end of aish, within the same limit.
//
// The text goes as the brief task gives, not as a request: an @file in it is
// not attached, nor a /skill, as Start leaves them for a subagent. The
// subagent reads the file with read_file, under the policy.
func (a *Agent) Spawn(name, prompt string, ex tools.Exec) (rpc.Task, error) {
	k := slices.IndexFunc(a.subs, func(d subagent.Def) bool { return d.Name == name })
	if k < 0 {
		names := make([]string, len(a.subs))
		for i, d := range a.subs {
			names[i] = d.Name
		}
		known := "none"
		if len(names) > 0 {
			known = strings.Join(names, ", ")
		}
		return rpc.Task{}, fmt.Errorf("no subagent %s here (there are %s; aish agents tells of the files)", name, known)
	}
	if strings.TrimSpace(prompt) == "" {
		return rpc.Task{}, fmt.Errorf("no text for %s: &%s TEXT", name, name)
	}
	a.exec = ex
	run := a.prepSub(a.subs[k], prompt)
	s := a.background()
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.unfinished(); n >= maxUnfinished {
		return rpc.Task{}, fmt.Errorf("%d subagents are at work in the background, and at most %d may be: aish tasks lists them", n, maxUnfinished)
	}
	return s.add([]*subRun{run}, true)[0].task(), nil
}

// Ended takes the user's tasks in the background that ended since it last
// gave them, in the order they ended: the prompt tells of each once.
func (a *Agent) Ended() []rpc.Task {
	s := a.madeBackground()
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []rpc.Task
	for _, id := range s.finished {
		if j := s.jobs[id]; j.user && !j.told {
			j.told = true
			out = append(out, j.task())
		}
	}
	return out
}
