package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// questionProxy answers the rpc of the commands at $AISH_SOCK with answer,
// which gets the call's ctx: the proxy's question waits for the user so.
func questionProxy(t *testing.T, answer func(ctx context.Context, method string) (any, error)) {
	t.Helper()
	l, err := net.Listen("unix", filepath.Join(t.TempDir(), "sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go rpc.Serve(l, func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
		return answer(ctx, method)
	})
	t.Setenv("AISH_SOCK", l.Addr().String())
}

// trustDir is a repository, the work directory, whose project file sets a
// hooks_dir; trusted.json is the test's.
func trustDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(repo, config.ProjectFile)
	if err := os.WriteFile(file, []byte("hooks_dir = \".aish/hooks\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	return file
}

// aish trust inside aish is the proxy's: it asks, and trusts the file on
// Yes; the command does not write trusted.json itself, on No or with a
// proxy older than the question.
func TestTrustByProxy(t *testing.T) {
	file := trustDir(t)
	got := fakeProxy(t, map[string]any{rpc.MethodTrust: rpc.Trusted{Path: file, Keys: []string{`hooks_dir = ".aish/hooks"`}}})
	code, stdout, stderr := captured(t, func() int { return trustCmd(config.Default(), nil) })
	if code != 0 || stderr != "" || !strings.Contains(stdout, "is trusted until it or its hooks and tools change") ||
		!strings.Contains(stdout, "\n  hooks_dir = \".aish/hooks\"\n") || !strings.Contains(stdout, "aish apply-config puts it in force here") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	var tp rpc.TrustParams
	if err := json.Unmarshal(got[rpc.MethodTrust], &tp); err != nil || tp.Cwd != filepath.Dir(file) {
		t.Errorf("params %s", got[rpc.MethodTrust])
	}
	if _, err := os.Stat(config.TrustFile()); err == nil {
		t.Error("the command wrote trusted.json, not the proxy")
	}

	for _, tc := range []struct{ answer, says string }{
		{"not confirmed; ~/repo/.aish.toml not trusted", "aish: not confirmed; ~/repo/.aish.toml not trusted\n"},
		{`unknown method "trust"`, "restart aish to trust here"},
	} {
		fakeProxy(t, map[string]any{rpc.MethodTrust: errors.New(tc.answer)})
		code, stdout, stderr := captured(t, func() int { return trustCmd(config.Default(), nil) })
		if code != 1 || stdout != "" || !strings.Contains(stderr, tc.says) || config.Trusted(file) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q, trusted %v", tc.answer, code, stdout, stderr, config.Trusted(file))
		}
	}
}

// aish trust --revoke inside aish only takes trust back: it asks nothing.
func TestTrustRevokeUnasked(t *testing.T) {
	file := trustDir(t)
	if err := config.Trust(file); err != nil {
		t.Fatal(err)
	}
	got := fakeProxy(t, map[string]any{rpc.MethodInfo: rpc.Info{Model: "m"}})
	code, stdout, stderr := captured(t, func() int { return trustCmd(config.Default(), []string{"--revoke"}) })
	if code != 0 || stderr != "" || !strings.Contains(stdout, "is not trusted") || config.Trusted(file) {
		t.Errorf("exit %d, stdout %q, stderr %q, trusted %v", code, stdout, stderr, config.Trusted(file))
	}
	if _, asked := got[rpc.MethodTrust]; asked {
		t.Error("asked the proxy")
	}
}

// aish apply-config tells the No and that nothing was applied.
func TestApplyConfigDeclined(t *testing.T) {
	diskConfig(t, "")
	fakeProxy(t, map[string]any{rpc.MethodApplyConfig: errors.New("not confirmed; nothing applied")})
	code, stdout, stderr := captured(t, func() int { return run([]string{"apply-config"}) })
	if code != 1 || stdout != "" || stderr != "aish: not confirmed; nothing applied\n" {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// Ctrl+C gives up on the question of aish apply-config: the call ends in
// the proxy, which takes the question off the screen before it answers,
// and only then aish apply-config says nothing was applied.
func TestApplyConfigInterrupted(t *testing.T) {
	diskConfig(t, "")
	erased := make(chan struct{})
	questionProxy(t, func(ctx context.Context, _ string) (any, error) {
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT) // the client is waiting, its handler set
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			return nil, errors.New("the call went on in the proxy")
		}
		time.Sleep(50 * time.Millisecond) // the question going off the screen
		close(erased)
		return nil, ctx.Err()
	})
	code, stdout, stderr := captured(t, func() int { return run([]string{"apply-config"}) })
	if code != 130 || stdout != "" || !strings.Contains(stderr, "interrupted; nothing applied") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	select {
	case <-erased:
	default:
		t.Error("aish apply-config went on before the proxy answered")
	}
}

// aish apply-config and aish trust wait for the answer to the proxy's
// question past rpc.CallTimeout, which bounds the calls answered at once.
// The timeout is the real one, so the test takes its ten seconds, for
// both at once.
func TestConfirmCallsWait(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out rpc.CallTimeout")
	}
	diskConfig(t, "")
	file := trustDir(t)
	questionProxy(t, func(ctx context.Context, method string) (any, error) {
		select {
		case <-time.After(rpc.CallTimeout + 500*time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if method == rpc.MethodTrust {
			return rpc.Trusted{Path: file}, nil
		}
		return rpc.Applied{}, nil
	})
	var codes [2]int
	_, stdout, stderr := captured(t, func() int {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); codes[0] = applyConfigCmd(nil) }()
		go func() { defer wg.Done(); codes[1] = trustCmd(config.Default(), nil) }()
		wg.Wait()
		return 0
	})
	if codes != [2]int{0, 0} || stderr != "" || !strings.Contains(stdout, "applied; nothing changed") ||
		!strings.Contains(stdout, "is trusted; it sets nothing that runs code") {
		t.Errorf("exit %v, stdout %q, stderr %q", codes, stdout, stderr)
	}
}
