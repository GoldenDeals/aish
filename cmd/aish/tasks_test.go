package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"

	"github.com/inebotov/aish/internal/rpc"
)

func TestPrintTasks(t *testing.T) {
	list := []rpc.Task{
		{ID: "bg1", Agent: "reviewer", State: "running", Prompt: "Review  the diff\nof the branch"},
		{ID: "bg10", Agent: "x", State: "ok"},
	}
	var b bytes.Buffer
	printTasks(&b, list, 40)
	want := "\x1b[1mbg1\x1b[0m   reviewer  running  Review the di…\n" +
		"\x1b[1mbg10\x1b[0m  x         ok\n"
	if b.String() != want {
		t.Errorf("printed\n%q\nwant\n%q", b.String(), want)
	}
	b.Reset()
	printTasks(&b, list[:1], 0)
	if want := "\x1b[1mbg1\x1b[0m  reviewer  running  Review the diff\n"; b.String() != want {
		t.Errorf("without a width: %q", b.String())
	}
	b.Reset()
	printTasks(&b, nil, 80)
	if b.String() != "\x1b[2m(no subagents in the background)\x1b[0m\n" {
		t.Errorf("none: %q", b.String())
	}
}

// aish tasks asks the proxy for the list, aish tasks show for one; other
// arguments ask for nothing.
func TestTasksCmd(t *testing.T) {
	l, err := net.Listen("unix", filepath.Join(t.TempDir(), "sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var asked []string
	go rpc.Serve(l, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != rpc.MethodTasks {
			return nil, errors.New("unexpected " + method)
		}
		var tp rpc.TasksParams
		if len(params) > 0 {
			json.Unmarshal(params, &tp)
		}
		asked = append(asked, tp.ID)
		switch tp.ID {
		case "":
			return []rpc.Task{{ID: "bg1", Agent: "a", State: "ok"}}, nil
		case "bg1":
			return rpc.Task{ID: "bg1", Agent: "a", State: "ok", Output: "out"}, nil
		}
		return nil, errors.New("unknown id " + tp.ID)
	})
	t.Setenv("AISH_SOCK", l.Addr().String())
	for _, c := range []struct {
		args []string
		code int
	}{
		{nil, 0},
		{[]string{"show", "bg1"}, 0},
		{[]string{"show", "bg2"}, 1},
		{[]string{"show"}, 1},
		{[]string{"bg1"}, 1},
	} {
		if code := tasksCmd(c.args); code != c.code {
			t.Errorf("%q: exit %d, want %d", c.args, code, c.code)
		}
	}
	if len(asked) != 3 || asked[0] != "" || asked[1] != "bg1" || asked[2] != "bg2" {
		t.Errorf("asked for %q", asked)
	}
}

func TestPrintTask(t *testing.T) {
	var b bytes.Buffer
	printTask(&b, rpc.Task{ID: "bg1", Agent: "a", State: "running"})
	if want := "\x1b[0m\x1b[36mbg1 a (running)\x1b[0m\n\x1b[0m\x1b[2m(no output)\x1b[0m\n"; b.String() != want {
		t.Errorf("printed %q, want %q", b.String(), want)
	}
	b.Reset()
	printTask(&b, rpc.Task{ID: "bg1", Agent: "a", State: "ok", Output: "line\nlast"})
	if want := "\x1b[0m\x1b[36mbg1 a (ok)\x1b[0m\nline\nlast\n\x1b[0m"; b.String() != want {
		t.Errorf("printed %q, want %q", b.String(), want)
	}
}
