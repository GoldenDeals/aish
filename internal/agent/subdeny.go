package agent

import (
	"reflect"
	"strings"

	"github.com/GoldenDeals/aish/internal/subagent"
	"github.com/GoldenDeals/aish/internal/tools"
)

// defTools is the registry of subagent d and what its bash may run (nil
// for anything): the tools its tools field gives (subTools), less those its
// disallowedTools and permissionMode take away. Neither of them gives back
// what tools leaves out, and aish yolo, which lifts the scope of the bash,
// does not bring back a tool they took.
func defTools(host *tools.Registry, d subagent.Def) (*tools.Registry, *bashScope) {
	reg, scope := subTools(host, d.Tools)
	deny := denyTools(d.Disallowed)
	plan := d.Mode == subagent.Plan
	out := &tools.Registry{}
	var kept *bashScope
	for _, t := range reg.All() {
		if b, ok := t.(subBash); ok {
			s, keep := deny.bash(scope, plan)
			if keep {
				out.Add(subBash{Tool: b.Tool, scope: s})
				kept = s
			}
			continue
		}
		if deny.takes(t) || plan && !planKeeps(t) {
			continue
		}
		out.Add(t)
	}
	return out, kept
}

// toolDenial is what the disallowedTools of a file take away.
type toolDenial struct {
	names  map[string]bool // aish's names, in lower case
	skills bool
	mcps   [][2]string // server and tool of the mcp__ entries; server * for all
	// bashWhole is Bash, with a pattern or without: the whole tool goes, the
	// read-only one of Grep, Glob and LS too, which is bash in aish.
	bashWhole bool
	// search is Grep, Glob or LS: the bash they give.
	search bool
}

// denyTools reads disallowedTools as subTools reads tools: Claude Code's
// names or aish's, in any case. An entry with a pattern takes the whole
// tool, Bash(git push *) the whole bash, as in Claude Code: a tool cannot
// be left without the commands of a pattern.
func denyTools(names []string) toolDenial {
	d := toolDenial{names: map[string]bool{}}
	for _, n := range names {
		base, _, _ := strings.Cut(strings.TrimSpace(n), "(")
		key := strings.ToLower(strings.TrimSpace(base))
		if m, ok := claudeTools[key]; ok {
			key = m
		}
		switch {
		case key == tools.Bash:
			// By name too: whatever bash the registry has goes.
			d.bashWhole, d.names[key] = true, true
		case searchTools[key]:
			d.search = true
		case key == "skill":
			d.skills = true
		case strings.HasPrefix(key, "mcp__"):
			server, tool, _ := strings.Cut(key[len("mcp__"):], "__")
			d.mcps = append(d.mcps, [2]string{server, tool})
		case key != "":
			d.names[key] = true
		}
	}
	return d
}

// takes tells whether the denial takes t, a tool but bash.
func (d toolDenial) takes(t tools.Tool) bool {
	if d.names[strings.ToLower(t.Name())] || d.skills && reflect.TypeOf(t) == skillTool {
		return true
	}
	for _, m := range d.mcps {
		// mcp__* is every server's, whatever follows it.
		if m[0] == "*" && tools.ServerOf(t) != "" || mcpTool(t, m[0], m[1]) {
			return true
		}
	}
	return false
}

// bash is what is left of scope, the bash's as tools gives it (nil for all
// of it), with the denial and plan; false when no bash is left. Grep, Glob
// and LS take the read-only commands they give, not a bash given whole.
// Plan leaves only those: a Bash(...) pattern may give a command that
// writes, so a bash given by patterns alone goes.
func (d toolDenial) bash(scope *bashScope, plan bool) (*bashScope, bool) {
	if d.bashWhole {
		return nil, false
	}
	if d.search && scope != nil {
		if len(scope.patterns) == 0 {
			return nil, false
		}
		scope = &bashScope{patterns: scope.patterns}
	}
	if plan {
		if scope != nil && !scope.readOnly {
			return nil, false
		}
		return &bashScope{readOnly: true}, true
	}
	return scope, true
}

// readFileType is the type of the read_file tool.
var readFileType = func() reflect.Type {
	for _, t := range tools.Builtins() {
		if t.Name() == "read_file" {
			return reflect.TypeOf(t)
		}
	}
	return nil
}()

// planKeeps tells whether t, a tool but bash, is one plan leaves: read_file
// and the skills, which give text and do nothing. The rest may write, the
// tools of MCP servers and the user's too, as far as aish knows.
func planKeeps(t tools.Tool) bool {
	typ := reflect.TypeOf(t)
	return typ == skillTool || t.Name() == "read_file" && typ == readFileType && tools.ServerOf(t) == ""
}
