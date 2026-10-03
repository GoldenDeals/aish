package proxy

import (
	"errors"
	"os/exec"
	"testing"
)

func TestExitCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		want   int
	}{
		{"hangup", "kill -HUP $$", 129},
		{"killed", "kill -KILL $$", 137},
		{"exit", "exit 3", 3},
		{"success", "true", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, err := exitCode(exec.Command("sh", "-c", tc.script).Run())
			if err != nil {
				t.Fatalf("exitCode: %v", err)
			}
			if code != tc.want {
				t.Errorf("exitCode = %d, want %d", code, tc.want)
			}
		})
	}
}

func TestExitCodeOtherError(t *testing.T) {
	want := errors.New("bash vanished")
	code, err := exitCode(want)
	if err != want || code != 0 {
		t.Errorf("exitCode = %d, %v; want 0, %v", code, err, want)
	}
	// Not started at all: no ExitError either.
	runErr := exec.Command("/nonexistent/aish-test").Run()
	if code, err := exitCode(runErr); err != runErr || code != 0 {
		t.Errorf("exitCode(start failure) = %d, %v; want 0, %v", code, err, runErr)
	}
}
