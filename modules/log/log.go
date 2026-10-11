// Package log turns on slog, which the core keeps off, and writes it to a file. It doesn't listen to the bus:
// modules log by themselves with slog.Warn / Info / Debug, called at log time.
//
//	log:
//	  level: info    # warn | info | debug
//	  format: jsonl  # jsonl | text
//	  file: aish-%D.log  # %D date, %T time, %S session UUID, %% a "%"; relative to the working directory
//	                     # default: $XDG_STATE_HOME (else ~/.local/state)/.aish/logs/%D_%T_%S.log
package log

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GoldenDeals/aish-core/contracts/session"
	"github.com/GoldenDeals/aish-core/pkg/bus"
	"github.com/GoldenDeals/aish-core/pkg/config"
	"github.com/GoldenDeals/aish-core/pkg/module"
)

type mod struct{}

func init() { module.Register("log", &mod{}) }

// shortcut: records from the Init of modules sorted before "log" are dropped; set slog up earlier if one needs them.
func (m *mod) Init(_ bus.Bus, cfg config.Config) error {
	c := cfg.Map()
	var level slog.Level // info
	if v, ok := c["level"].(string); ok {
		if err := level.UnmarshalText([]byte(v)); err != nil {
			return err
		}
	}
	path, _ := c["file"].(string)
	if path == "" {
		dir := os.Getenv("XDG_STATE_HOME")
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			dir = filepath.Join(home, ".local", "state")
		}
		path = filepath.Join(dir, ".aish", "logs", "%D_%T_%S.log")
	}
	now := time.Now()
	path = strings.NewReplacer("%D", now.Format("2006-01-02"), "%T", now.Format("15-04-05"),
		"%S", session.ID().String(), "%%", "%").Replace(path)

	format, _ := c["format"].(string)
	if format != "" && format != "jsonl" && format != "text" {
		return fmt.Errorf("format %q: want jsonl or text", format)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// 0600: debug records hold the prompts. Never closed: modules that stop after this one still log, the OS closes it.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewJSONHandler(f, opts)
	if format == "text" {
		h = slog.NewTextHandler(f, opts)
	}
	slog.SetDefault(slog.New(h))
	return nil
}

func (m *mod) Start(context.Context) error { return nil }

func (m *mod) Stop(context.Context) error { return nil }
