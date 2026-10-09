package proxy

import "golang.org/x/sys/unix"

// canonical reports whether the terminal fd, a PTY's master side, whose
// termios is the shell's side's, reads lines (ICANON).
func canonical(fd int) (bool, error) {
	t, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return true, err
	}
	return t.Lflag&unix.ICANON != 0, nil
}
