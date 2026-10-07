//go:build !linux && !darwin

package rpc

import (
	"errors"
	"runtime"
)

// peerPID knows no client here: the proxy refuses what only the shell's
// foreground job may ask for.
func peerPID(int) (int, error) {
	return 0, errors.New("the client of a unix socket is not known on " + runtime.GOOS)
}
