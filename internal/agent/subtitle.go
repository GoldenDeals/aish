package agent

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/mattn/go-runewidth"
)

// The subagents of a call are told apart on the screen by their titles:
// five tasks of one subagent would be five panes, summaries and folds of
// the same name. A task may come with a description, a few words as with
// Claude Code, which goes after the name; a title the call repeats gets
// its number among them. Above the output of each goes the task itself.

// descMax is how many columns of a description a title keeps: it is
// meant to be a few words, and the summary of a pane is one line.
const descMax = 60

// taskDesc is the description of a task of the call, on one line and
// without control characters, which would reach the terminal of aish
// tasks; "" when it has none.
func taskDesc(m map[string]any) string {
	s, _ := m["description"].(string)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return runewidth.Truncate(strings.Join(strings.Fields(s), " "), descMax, "…")
}

// TaskTitle is subagent name with the description of its task, if any:
// the title of its pane, summary and fold, and of the task in aish tasks.
func TaskTitle(name, desc string) string {
	if desc == "" {
		return name
	}
	return name + ": " + desc
}

// taskTitles are the titles of the tasks of a call, in its order: those
// it repeats, the same subagent without a description say, numbered
// among themselves, runner #1 and runner #2.
func taskTitles(jobs []subJob) []string {
	out := make([]string, len(jobs))
	count := map[string]int{}
	for i, j := range jobs {
		out[i] = TaskTitle(j.def.Name, j.desc)
		count[out[i]]++
	}
	seen := map[string]int{}
	for i, t := range out {
		if count[t] > 1 {
			seen[t]++
			out[i] = fmt.Sprintf("%s #%d", t, seen[t])
		}
	}
	return out
}

// QuoteTask is the task a subagent was given with each of its lines after
// "> ", as a mail quotes: above its output, apart from it. "" for a task
// of blanks.
func QuoteTask(prompt string) string {
	prompt = strings.Trim(prompt, "\n")
	if strings.TrimSpace(prompt) == "" {
		return ""
	}
	lines := strings.Split(prompt, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight("> "+l, " \t")
	}
	return strings.Join(lines, "\n")
}
