package main

import (
	"testing"
	"time"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/session"
)

func TestParsePrune(t *testing.T) {
	off, month := config.Default(), config.Default()
	month.SessionsTTL = "30d"
	day := 24 * time.Hour
	for _, c := range []struct {
		cfg  config.Config
		args []string
		want time.Duration
	}{
		{month, nil, 30 * day},
		{off, []string{"--older", "12h"}, 12 * time.Hour},
		{month, []string{"--older=0d"}, 0},
	} {
		if got, err := parsePrune(c.cfg, c.args); err != nil || got != c.want {
			t.Errorf("%q: %v %v", c.args, got, err)
		}
	}
	for _, args := range [][]string{nil, {"--older"}, {"--older", "month"}, {"--older", "-1d"}, {"30d"}} {
		if got, err := parsePrune(off, args); err == nil {
			t.Errorf("%q: %v", args, got)
		}
	}
}

func TestPickerDelete(t *testing.T) {
	dir := t.TempDir()
	for range 2 {
		s, err := session.New(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
		s.Unlock()
	}
	list, _ := session.List(dir)
	p := &picker{dir: dir, list: list, cur: list[1].ID}

	p.sel = 1
	if p.key("d"); p.deleting {
		t.Fatal("asks to delete the session of this shell")
	}
	p.sel = 0
	p.key("d")
	if done, _ := p.key("n"); done || len(p.list) != 2 {
		t.Fatalf("deleted on n: %d left", len(p.list))
	}
	p.cur = ""
	p.key("d")
	if done, _ := p.key("y"); done || len(p.list) != 1 {
		t.Fatalf("y: done %v, %d left", done, len(p.list))
	}
	p.key("d")
	if done, ok := p.key("y"); !done || ok {
		t.Errorf("the last deleted: done %v, ok %v", done, ok)
	}
	if left, _ := session.List(dir); len(left) != 0 {
		t.Errorf("left on disk %+v", left)
	}
}
