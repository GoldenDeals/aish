package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/rpc"
	"github.com/GoldenDeals/aish/internal/session"
)

// sessionRenameCmd gives a session the user's name, which `aish resume`
// lists it by: `aish session rename NAME` this shell's session, `aish
// session rename ID|NAME NEWNAME` another, found by its id, the user's
// name or the model's. A name "" takes the user's away.
func sessionRenameCmd(cfg config.Config, args []string) int {
	usage := errors.New("usage: aish session rename [ID|NAME] NEWNAME (a name of several words in quotes)")
	if len(args) == 0 || len(args) > 2 {
		return fail(usage)
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return fail(usage)
		}
	}
	dir, info, err := sessionsDir(cfg)
	if err != nil {
		return fail(err)
	}
	client, _ := rpc.FromEnv() // nil outside aish
	id, name := info.SessionID, strings.TrimSpace(args[len(args)-1])
	if len(args) == 2 {
		list, err := session.List(dir)
		if err != nil {
			return fail(err)
		}
		i, err := session.FindAll(list, args[0])
		if err != nil {
			return fail(err)
		}
		id = i.ID
	} else if client == nil {
		return fail(errors.New("no session here: outside aish, aish session rename ID|NAME NEWNAME"))
	}
	if err := renameSession(client, dir, info.SessionID, id, name); err != nil {
		return fail(err)
	}
	if name == "" {
		fmt.Printf("session %s has no name now\n", id)
	} else {
		fmt.Printf("session %s is %s now\n", id, name)
	}
	return 0
}

// renameSession gives session id of dir the user's name: the session of
// this shell, cur, through the proxy, which holds its lock and keeps the
// name of one not on disk yet; another under its lock, which refuses one
// open in another aish.
func renameSession(client *rpc.Client, dir, cur, id, name string) error {
	if client != nil && id == cur {
		var info rpc.Info
		return client.Call(rpc.MethodRename, rpc.RenameParams{Name: name}, &info)
	}
	return session.Rename(dir, id, name)
}
