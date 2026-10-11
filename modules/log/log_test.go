package log

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish-core/contracts/session"
	corebus "github.com/GoldenDeals/aish-core/internal/core/bus"
)

type cfg map[string]any

func (c cfg) Map() map[string]any { return c }

func TestInit(t *testing.T) {
	id := session.ID().String()
	for name, tc := range map[string]struct {
		cfg  func(t *testing.T, dir string) cfg
		file string   // relative to dir, "" means Init fails
		want []string // in the file
		not  []string // not in the file
		json bool
	}{
		"debug text": {
			cfg: func(t *testing.T, dir string) cfg {
				return cfg{"file": dir + "/%S-%%.log", "level": "debug", "format": "text"}
			},
			file: id + "-%.log", want: []string{"level=DEBUG", "level=INFO", "level=WARN"},
		},
		"warn": {
			cfg:  func(t *testing.T, dir string) cfg { return cfg{"file": dir + "/a.log", "level": "warn"} },
			file: "a.log", want: []string{"WARN"}, not: []string{"INFO", "DEBUG"}, json: true,
		},
		"default": {
			cfg:  func(t *testing.T, dir string) cfg { t.Setenv("XDG_STATE_HOME", dir); return cfg{} },
			file: ".aish/logs/*_" + id + ".log", want: []string{"INFO", "WARN"}, not: []string{"DEBUG"}, json: true,
		},
		"bad level":  {cfg: func(t *testing.T, dir string) cfg { return cfg{"file": dir + "/a.log", "level": "loud"} }},
		"bad format": {cfg: func(t *testing.T, dir string) cfg { return cfg{"file": dir + "/a.log", "format": "xml"} }},
	} {
		t.Run(name, func(t *testing.T) {
			old := slog.Default()
			t.Cleanup(func() { slog.SetDefault(old) })
			dir := t.TempDir()
			err := (&mod{}).Init(corebus.New(), tc.cfg(t, dir))
			if tc.file == "" {
				if err == nil {
					t.Error("Init succeeded")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			slog.Debug("d")
			slog.Info("i")
			slog.Warn("w")

			files, _ := filepath.Glob(filepath.Join(dir, tc.file))
			if len(files) != 1 {
				t.Fatalf("files %q, want one %s", files, tc.file)
			}
			b, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tc.want {
				if !strings.Contains(string(b), w) {
					t.Errorf("no %s in %s", w, b)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(string(b), n) {
					t.Errorf("%s in %s", n, b)
				}
			}
			for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				if tc.json && !json.Valid([]byte(l)) {
					t.Errorf("not json: %s", l)
				}
			}
		})
	}
}
