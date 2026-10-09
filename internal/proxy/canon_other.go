//go:build !linux && !darwin

package proxy

import (
	"errors"
	"runtime"
)

// canonical cannot tell the terminal's mode here: Esc during a request is
// aish's even while a program the agent runs reads the keys one by one.
func canonical(int) (bool, error) {
	return true, errors.New("the terminal's mode is not known on " + runtime.GOOS)
}
