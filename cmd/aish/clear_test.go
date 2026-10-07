package main

import (
	"testing"

	"github.com/GoldenDeals/aish/internal/rpc"
)

func TestParseClear(t *testing.T) {
	for _, c := range []struct {
		args []string
		want rpc.ClearParams
	}{
		{nil, rpc.ClearParams{}},
		{[]string{"save"}, rpc.ClearParams{Save: true}},
		{[]string{"save", "a", "b"}, rpc.ClearParams{Save: true, Name: "a b"}},
	} {
		if got, err := parseClear(c.args); err != nil || got != c.want {
			t.Errorf("%q: %+v %v", c.args, got, err)
		}
	}
	for _, args := range [][]string{{"safe"}, {"save", "-h"}} {
		if got, err := parseClear(args); err == nil {
			t.Errorf("%q: %+v", args, got)
		}
	}
}

func TestStartedOver(t *testing.T) {
	old, saved, info := rpc.Info{SessionID: "a"}, rpc.Info{SessionID: "a", Saved: true}, rpc.Info{SessionID: "b"}
	for _, c := range []struct {
		cp   rpc.ClearParams
		old  rpc.Info
		want string
	}{
		{rpc.ClearParams{}, old, "dropped a, new session b"},
		{rpc.ClearParams{}, saved, "left a, new session b"},
		{rpc.ClearParams{Save: true}, old, "saved a, new session b"},
		{rpc.ClearParams{Save: true, Name: "x y"}, old, "saved a as x y, new session b"},
		{rpc.ClearParams{SaveNew: true}, old, "new session b (saved)"},
		{rpc.ClearParams{SaveNew: true, NewName: "work"}, saved, "new session work (b, saved)"},
	} {
		if got := startedOver(c.cp, c.old, info); got != c.want {
			t.Errorf("%+v: %q, want %q", c.cp, got, c.want)
		}
	}
}
