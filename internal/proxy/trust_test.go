package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/rpc"
)

type trustResult struct {
	res rpc.Trusted
	err error
}

// trustRepo is a proxy and a repository in its work directory whose
// project file sets body; trusted.json is the test's.
func trustRepo(t *testing.T, body string) (*Proxy, string) {
	t.Helper()
	p, _, cwd := hosted(t, &scripted{})
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	if err := os.Mkdir(filepath.Join(cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(cwd, config.ProjectFile)
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	withTerminal(p)
	p.marker(Marker{Kind: "cmd-end", Payload: "0;" + cwd})
	return p, file
}

// trustCall is rpc trust from the shell's work directory, as its
// foreground job; the result comes on the channel.
func trustCall(p *Proxy) <-chan trustResult {
	res := make(chan trustResult, 1)
	go func() {
		b, _ := json.Marshal(rpc.TrustParams{Cwd: filepath.Join(os.Getenv("HOME"), "work")})
		v, err := p.handle(rpc.WithPeer(context.Background(), os.Getpid()), rpc.MethodTrust, b)
		r, _ := v.(rpc.Trusted)
		res <- trustResult{r, err}
	}()
	return res
}

func trustedNow(t *testing.T, res <-chan trustResult) (rpc.Trusted, error) {
	t.Helper()
	select {
	case r := <-res:
		return r.res, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("rpc trust never returned")
		return rpc.Trusted{}, nil
	}
}

// aish trust inside aish asks, naming the file and its keys that run
// code, No chosen at first: Yes trusts the file, No leaves trusted.json
// as it was.
func TestTrustConfirm(t *testing.T) {
	for _, key := range []string{"y", "n", "\r"} {
		firmNow(t)
		p, file := trustRepo(t, "hooks_dir = \".aish/hooks\"\n")
		res := trustCall(p)
		waitOpen(t, p, func() bool { return p.ask != nil })
		if s := p.out.(*terminal).String(); !strings.Contains(s, "~/work/.aish.toml: these keys run code from the repository") ||
			!strings.Contains(s, "\r\n  hooks_dir = \".aish/hooks\"\r\n") || !strings.Contains(s, "Trust it?\x1b[0m "+choices("No")) {
			t.Errorf("%q: the question %q", key, s)
		}
		pause(p)
		p.key([]byte(key))
		got, err := trustedNow(t, res)
		if key == "y" {
			if err != nil || got.Path != file || !slices.Equal(got.Keys, []string{"hooks_dir = \".aish/hooks\""}) || !config.Trusted(file) {
				t.Errorf("Yes: %v, %+v, trusted %v", err, got, config.Trusted(file))
			}
			continue
		}
		if err == nil || err.Error() != "not confirmed; ~/work/.aish.toml not trusted" || config.Trusted(file) {
			t.Errorf("%q: %v, trusted %v", key, err, config.Trusted(file))
		}
		if _, err := os.Stat(config.TrustFile()); err == nil {
			t.Errorf("%q: trusted.json written", key)
		}
	}
}

// The file is trusted as the question showed it: edited while it is open,
// it is not trusted.
func TestTrustEditedWhileAsked(t *testing.T) {
	firmNow(t)
	p, file := trustRepo(t, "hooks_dir = \".aish/hooks\"\n")
	res := trustCall(p)
	waitOpen(t, p, func() bool { return p.ask != nil })
	if err := os.WriteFile(file, []byte("tools_dir = \"/tmp\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pause(p)
	p.key([]byte("y"))
	if _, err := trustedNow(t, res); err == nil || !strings.Contains(err.Error(), "changed since; not trusted") || config.Trusted(file) {
		t.Errorf("%v, trusted %v", err, config.Trusted(file))
	}
}

// The assistant's aish trust, and one in the background, are refused
// before anything is asked; so is a directory without a project file, or
// with one aish refuses.
func TestTrustRefusedUnasked(t *testing.T) {
	p, file := trustRepo(t, "hooks_dir = \".aish/hooks\"\n")
	p.marker(Marker{Kind: "ask-start"})
	if _, err := trustedNow(t, trustCall(p)); !errors.Is(err, errTrustAsks) {
		t.Errorf("the assistant: %v", err)
	}
	p.marker(Marker{Kind: "cmd-end", Payload: "0;/tmp"})
	p.mu.Lock()
	fg := p.fg
	p.fg = func() (int, error) { n, _ := fg(); return n + 1, nil }
	p.mu.Unlock()
	if _, err := trustedNow(t, trustCall(p)); !errors.Is(err, errNotShell) {
		t.Errorf("from the background: %v", err)
	}
	p.mu.Lock()
	p.fg = fg
	p.mu.Unlock()
	if err := os.WriteFile(file, []byte("model = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := trustedNow(t, trustCall(p)); err == nil {
		t.Error("a file aish refuses: trusted")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err := trustedNow(t, trustCall(p)); err == nil || !strings.Contains(err.Error(), "no .aish.toml here") {
		t.Errorf("no file: %v", err)
	}
	if s := p.out.(*terminal).String(); strings.Contains(s, "Trust it?") || p.ask != nil || config.Trusted(file) {
		t.Errorf("asked: %q", s)
	}
}
