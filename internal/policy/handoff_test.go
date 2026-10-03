package policy

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// callInput builds the input of a call as the agent does: bash hands its
// command to the shell, as shell.Command gives it. The shell has HOME of
// the test, and no PWD or CDPATH of the one running it.
func callInput(tool string, args map[string]any, cwd string) Input {
	in := NewInput(tool, args, cwd, []string{"HOME=" + os.Getenv("HOME")})
	if c, ok := args["command"].(string); ok && tool == "bash" && strings.TrimSpace(c) != "" {
		in.HandOff(c)
	}
	return in
}

// A tool of another name than bash handing a command to the shell is
// judged by the commands of that line, by the rules and by Cedar alike;
// without the line it is a call like any other.
func TestHandOff(t *testing.T) {
	ctx := context.Background()
	rules, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}, Ask: []string{"echo 'oops"}})
	if err != nil {
		t.Fatal(err)
	}
	example, err := Load(ctx, filepath.Join("..", "..", "examples", "policy"), Rules{})
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	ssh := func(line string) Input {
		in := Input{Tool: "ssh", Args: map[string]any{"host": "box", "what": line}, Cwd: home, Home: home}
		if line != "" {
			in.HandOff(line)
		}
		return in
	}
	for _, c := range []struct {
		name   string
		e      *Engine
		in     Input
		want   string
		reason string
	}{
		{"rules", rules, ssh("sudo ls"), Deny, `matches "sudo *"`},
		{"example", example, ssh("sudo ls"), Deny, "sudo is not allowed for the agent"},
		{"rules without the line", rules, ssh(""), Allow, ""},
		{"example without the line", example, ssh(""), Allow, ""},
		// What does not parse is matched as one command.
		{"rules, unparsed", rules, ssh("echo 'oops"), Ask, `matches "echo 'oops"`},
		{"example, unparsed", example, ssh("echo 'oops"), Ask, "could not parse the command"},
	} {
		d, err := c.e.Check(ctx, c.in)
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.want || d.Reason != c.reason {
			t.Errorf("%s: %s (%s), want %s (%s)", c.name, d.Action, d.Reason, c.want, c.reason)
		}
	}

	in := ssh("echo 'oops")
	if in.Line != "echo 'oops" || in.ParseError == "" || len(in.Commands) != 0 {
		t.Errorf("unparsed line: %+v", in)
	}
	in = ssh("sudo ls")
	if want := [][]string{{"sudo", "ls"}, {"ls"}}; !reflect.DeepEqual(in.Commands, want) || in.ParseError != "" {
		t.Errorf("commands %q (%s), want %q", in.Commands, in.ParseError, want)
	}
}

// Cedar sees the tool and the line handed off, so that a policy can tell a
// remote shell from bash.
func TestHandOffContext(t *testing.T) {
	e := mustLoad(t, map[string]string{"a.cedar": permitAll +
		`@reason("not on the box") forbid(principal, action == Action::"run", resource == Command::"rm") when { context.tool == "ssh" && context.line like "*-rf*" };` + "\n"})
	remote := Input{Tool: "ssh", Cwd: "/", Home: "/home/u"}
	remote.HandOff("cd /srv && rm -rf cache")
	if d := check(t, e, remote); d.Action != Deny || d.Reason != "not on the box" {
		t.Errorf("ssh: %+v", d)
	}
	if d := check(t, e, bash("cd /srv && rm -rf cache")); d.Action != Allow {
		t.Errorf("bash: %+v", d)
	}
}
