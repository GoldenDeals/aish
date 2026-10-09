package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// shortTimeout makes a command time out in d for the test.
func shortTimeout(t *testing.T, d time.Duration) {
	old := commandTimeout
	commandTimeout = d
	t.Cleanup(func() { commandTimeout = old })
}

func TestResolve(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AISH_TEST_HOST", "grafana.example")
	s := Server{
		Env: map[string]string{"URL": "https://${AISH_TEST_HOST}/x"},
		EnvCommand: map[string]string{
			"TOKEN": `printf 'tok\n'`,
			"CRLF":  `printf 'a\r\n'`,
			"TWO":   `printf 'a\n\n'`,
			"DIR":   `pwd`,
			"HOST":  `printf %s "$AISH_TEST_HOST"`,
		},
		Headers:        map[string]string{"X-Host": "${AISH_TEST_HOST}"},
		HeadersCommand: map[string]string{"Authorization": `echo "Bearer $(printf s3)"`},
	}
	env, headers, err := resolve(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"URL": "https://grafana.example/x", "TOKEN": "tok", "CRLF": "a", "TWO": "a\n", "DIR": home, "HOST": "grafana.example",
	} {
		if env[k] != want {
			t.Errorf("env %s = %q, want %q", k, env[k], want)
		}
	}
	if headers["X-Host"] != "grafana.example" || headers["Authorization"] != "Bearer s3" || len(headers) != 2 {
		t.Errorf("headers %q", headers)
	}
}

func TestResolveErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		s    Server
		want string
	}{
		{Server{EnvCommand: map[string]string{"TOKEN": "echo nope >&2; exit 3"}}, "env_command TOKEN: exit status 3: nope"},
		{Server{EnvCommand: map[string]string{"TOKEN": "true"}}, "env_command TOKEN: no output"},
		{Server{EnvCommand: map[string]string{"TOKEN": "echo"}}, "env_command TOKEN: no output"},
		// No input: cat ends at once instead of waiting for the terminal.
		{Server{EnvCommand: map[string]string{"TOKEN": "cat"}}, "env_command TOKEN: no output"},
		{Server{HeadersCommand: map[string]string{"Authorization": "printf 'a\\nb'"}}, "headers_command Authorization: more than one line"},
		// What a command printed is masked in the error of the next one.
		{Server{EnvCommand: map[string]string{"A": "printf v4lue", "B": "echo got v4lue >&2; exit 1"}},
			"env_command B: exit status 1: got ***"},
	} {
		_, _, err := resolve(context.Background(), tc.s)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%+v: %v, want %q", tc.s, err, tc.want)
		}
	}
}

// A command that hangs is killed at the timeout with all it started.
func TestResolveTimeout(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	shortTimeout(t, 200*time.Millisecond)
	pidFile := filepath.Join(dir, "pid")
	start := time.Now()
	_, _, err := resolve(context.Background(), Server{EnvCommand: map[string]string{
		"TOKEN": "sleep 5 & echo $! >" + pidFile + "; echo waiting >&2; wait",
	}})
	if err == nil || err.Error() != "env_command TOKEN: timed out: waiting" {
		t.Errorf("hung command: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v", d)
	}
	b, _ := os.ReadFile(pidFile)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("pid of sleep: %q", b)
	}
	// Reaped by init once killed; a zombie answers signal 0 until then.
	for deadline := time.Now().Add(2 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("sleep %d outlived the command", pid)
		}
	}
}

// The value of a command is masked in the error of the server's start,
// its key a word no secret has, wherever the error goes: `aish mcp`
// (Status), `aish tool` (List).
func TestCommandMaskedStart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const secret = "v4lue-0f-x"
	m := NewManager(map[string]Server{"bad": {
		Command:    "/bin/sh",
		Args:       []string{"-c", `echo "cannot log in with $AISH_PLAIN" >&2; exit 1`},
		EnvCommand: map[string]string{"AISH_PLAIN": "printf " + secret},
	}}, t.TempDir())
	defer m.Close()
	res := m.List(context.Background(), true)
	st := m.Status().Servers[0]
	for what, msg := range map[string]string{"list": strings.Join(res.Errors, "\n"), "status": st.Error} {
		if strings.Contains(msg, secret) || !strings.Contains(msg, "cannot log in with ***") {
			t.Errorf("%s: %q, want the value masked", what, msg)
		}
	}
	if st.State != "failed" {
		t.Errorf("state %q", st.State)
	}
}

// The error of a call, which goes to the model, has the value masked too:
// the server that died quotes it in the end of its stderr.
func TestCommandMaskedCall(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	exe, _ := os.Executable()
	m := NewManager(map[string]Server{"stub": {
		Command:    exe,
		Env:        map[string]string{"AISH_MCP_STUB": "1", "AISH_MCP_STARTS": filepath.Join(dir, "starts")},
		EnvCommand: map[string]string{"AISH_PLAIN": "printf cheese"},
	}}, filepath.Join(dir, "cache"))
	defer m.Close()
	_, err := m.Call(context.Background(), "stub_crash", nil)
	if err == nil || strings.Contains(err.Error(), "cheese") || !strings.Contains(err.Error(), "boom: out of ***") {
		t.Errorf("crash: %v, want the value masked", err)
	}
}

// The stub server gets the variable a command gave: it writes its log
// where that variable says.
func TestEnvCommandServer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	exe, _ := os.Executable()
	m := NewManager(map[string]Server{"stub": {
		Command:    exe,
		Env:        map[string]string{"AISH_MCP_CLIENT_STUB": "1"},
		EnvCommand: map[string]string{"AISH_MCP_LOG": "printf '%s\\n' '" + filepath.Join(dir, "log") + "'"},
	}}, filepath.Join(dir, "cache"))
	defer m.Close()
	if res := m.List(context.Background(), true); len(res.Tools) != 1 || len(res.Errors) != 0 {
		t.Fatalf("listing: %+v", res)
	}
	if got := logLines(dir); len(got) != 1 || got[0] != "start" {
		t.Errorf("log %q, want the stub's start", got)
	}
}

// A header a command gave goes with every request.
func TestHeadersCommandServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var (
		mu   sync.Mutex
		auth []string
	)
	h := &httpStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	m := NewManager(map[string]Server{"web": {
		URL:            srv.URL,
		HeadersCommand: map[string]string{"Authorization": `echo "Bearer $(printf t0ken)"`},
	}}, t.TempDir())
	defer m.Close()
	if res := m.List(context.Background(), true); len(res.Tools) != 1 || len(res.Errors) != 0 {
		t.Fatalf("listing: %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auth) == 0 {
		t.Fatal("no requests")
	}
	for _, a := range auth {
		if a != "Bearer t0ken" {
			t.Errorf("Authorization %q", a)
		}
	}
}

// Warm leaves a server with commands for its first use: pass must not ask
// for a passphrase as the shell starts.
func TestWarmSkipsCommands(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	ran := filepath.Join(dir, "ran")
	m := NewManager(map[string]Server{"s": {
		Command:    "/bin/sh",
		Args:       []string{"-c", "exit 1"},
		EnvCommand: map[string]string{"T": "touch " + ran + "; printf t"},
	}}, filepath.Join(dir, "cache"))
	defer m.Close()
	m.Warm()
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(ran); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the command ran at Warm: %v", err)
	}
	if st := m.Status().Servers[0]; st.State != "new" {
		t.Errorf("state %q, want new", st.State)
	}
	m.List(context.Background(), true)
	if _, err := os.Stat(ran); err != nil {
		t.Errorf("the command did not run at the first use: %v", err)
	}
}
