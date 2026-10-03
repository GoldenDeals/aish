package shellinit

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// cleanEnv is the environment of the test's process for a bash under test,
// without what aish puts there, then extra. Run inside aish, the tests
// would otherwise hand that bash the live session: init.bash would read
// its route, write its state.base and state, and empty its restore.bash.
func cleanEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "AISH_") || strings.HasPrefix(name, "__aish") ||
			strings.HasPrefix(name, "BASH_FUNC___aish") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

// TestMain runs the tests as if inside a session of a foreign aish, one
// that routes every unknown line to the assistant, and fails if any test
// touched its run directory: a bash under test that got the environment
// past cleanEnv would read and write it.
func TestMain(m *testing.M) {
	run, err := os.MkdirTemp("", "aish-foreign-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := guard(m, run)
	os.RemoveAll(run)
	os.Exit(code)
}

func guard(m *testing.M, run string) int {
	files := map[string]string{
		"route":        "capital=true\nnot_found=true\nsuffix=?\nmin_words=2\nexpand=true\n",
		"nonce":        "FOREIGN\n",
		"state.base":   "foreign base\n",
		"state":        "foreign state\n",
		"restore.bash": ": foreign restore\n",
		"next.cmd":     "",
		"next.id":      "",
		"aish":         "#!/bin/sh\necho \"$@\" >>\"$AISH_RUN/called\"\n",
	}
	for name, s := range files {
		if err := os.WriteFile(filepath.Join(run, name), []byte(s), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	for k, v := range map[string]string{
		"AISH_RUN":        run,
		"AISH_SOCK":       filepath.Join(run, "sock"),
		"AISH_BIN":        filepath.Join(run, "aish"),
		"AISH_SESSION":    "foreign",
		"AISH_TOOLS_PATH": filepath.Join(run, "bin"),
	} {
		os.Setenv(k, v)
	}
	before, err := snapshot(run)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	code := m.Run()
	after, err := snapshot(run)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var touched []string
	for name := range after {
		if after[name] != before[name] {
			touched = append(touched, name)
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			touched = append(touched, name)
		}
	}
	if len(touched) > 0 {
		sort.Strings(touched)
		fmt.Fprintf(os.Stderr, "FAIL: a test touched $AISH_RUN of the session it ran in: %s\n", strings.Join(touched, ", "))
		return 1
	}
	return code
}

// snapshot lists the files of dir with their contents and modification
// times: rewriting a file with the same bytes is a touch too.
func snapshot(dir string) (map[string]string, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	for _, de := range des {
		p := filepath.Join(dir, de.Name())
		fi, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		files[de.Name()] = fi.ModTime().Format(time.RFC3339Nano) + "\n" + string(data)
	}
	return files, nil
}
