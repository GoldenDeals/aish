package tools

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// homePath expands a leading ~ of a builtin's path. The home is the
// proxy's, os.UserHomeDir, and not HOME from ex.Env as the rest of Exec
// would suggest: the policy expands ~ with it too (policy.NewInput), and a
// shell with HOME=/etc must not get one path checked and another written.
func homePath(p string) string {
	rest, ok := strings.CutPrefix(p, "~")
	if !ok || rest != "" && !strings.HasPrefix(rest, "/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	return filepath.Join(home, rest)
}

// maxLinks is MAXSYMLINKS of Linux, as for the policy's resolve.
const maxLinks = 40

// writeAtomic replaces the file at path with data, whole or not at all. A
// symlink is followed to its target, which is replaced in its own
// directory, so that a link (a dotfile kept in a repository) stays a link.
// mode is that of a new file under the umask, as with os.WriteFile; an
// existing file is given mode as is, the umask has no say over it. A file
// that a replacement would not keep whole is written in place instead.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	target, err := linkTarget(path)
	if err != nil {
		return err
	}
	st, statErr := os.Stat(target)
	if statErr == nil {
		// A rename replaces a file the user may not write to as well, if
		// only the directory lets them: a read-only file stays a refusal.
		if err := syscall.Access(target, wOK); err != nil {
			return &os.PathError{Op: "open", Path: target, Err: err}
		}
		if inPlace(st) {
			return writeInPlace(target, st, data)
		}
	}
	dir, base := filepath.Split(target)
	f, err := createTemp(dir, "."+base+".aish-", mode)
	if statErr == nil && (errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EROFS)) {
		// The directory is closed to the user, the file is not: it can
		// still be written, only not replaced.
		return writeInPlace(target, st, data)
	}
	if err != nil {
		// The temporary name is not one the agent knows: the error names
		// the file it asked for.
		var pe *os.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return &os.PathError{Op: "write", Path: target, Err: err}
	}
	_, err = f.Write(data)
	if err == nil && statErr == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), target)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// wOK is W_OK of access(2), which package syscall does not name.
const wOK = 2

// inPlace tells whether a rename over the file would lose what belongs to
// the file rather than to its name: the content seen through its other
// hard links, a FIFO or a device (a plain file takes its place), an owner
// or group that is not the user's (the new file is theirs, in their
// group). The xattrs and ACL of a file the user owns go too: copying them
// to the new file would be platform-specific and still incomplete.
func inPlace(st os.FileInfo) bool {
	if !st.Mode().IsRegular() {
		return true
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	return ok && (uint64(sys.Nlink) > 1 || sys.Uid != uint32(os.Geteuid()) || sys.Gid != uint32(os.Getegid()))
}

// writeInPlace writes data over the existing file at target, which keeps
// its mode and everything else, but is not whole or not at all: a failed
// write leaves it cut short. A FIFO is opened without waiting for a
// reader, which may never come: the agent would wait in open(2), past any
// Ctrl+C, and every request after it would wait for that one.
func writeInPlace(target string, st os.FileInfo, data []byte) error {
	flag := os.O_WRONLY | os.O_TRUNC
	fifo := st.Mode()&os.ModeNamedPipe != 0
	if fifo {
		flag |= syscall.O_NONBLOCK
	}
	f, err := os.OpenFile(target, flag, 0)
	if fifo && errors.Is(err, syscall.ENXIO) {
		return fmt.Errorf("write %s: a FIFO with no reader", target)
	}
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// notRegular refuses a path that is neither a file nor a directory. A FIFO
// opened for reading waits in open(2) for a writer, which may never come:
// the agent would wait past any Ctrl+C, and every request after it would
// wait for that one. A device may have no end (/dev/zero) or wait for the
// user (/dev/tty). A directory or a missing path is left to open, which
// fails as it always has.
func notRegular(path string) error {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() || st.Mode().IsRegular() {
		return nil
	}
	return fmt.Errorf("%s: not a regular file", path)
}

// createTemp is os.CreateTemp that creates the file with mode under the
// umask rather than 0600, so that a new file gets the mode os.WriteFile
// would give it.
func createTemp(dir, prefix string, mode os.FileMode) (*os.File, error) {
	for range 10000 {
		name := dir + prefix + strconv.FormatUint(rand.Uint64(), 36)
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, mode)
		if !errors.Is(err, os.ErrExist) {
			return f, err
		}
	}
	return nil, fmt.Errorf("create a temporary file in %s: too many attempts", dir)
}

// linkTarget follows path while it is a symlink, dangling or not: rename(2)
// would replace the link itself.
func linkTarget(path string) (string, error) {
	for range maxLinks + 1 {
		st, err := os.Lstat(path)
		if err != nil || st.Mode()&os.ModeSymlink == 0 {
			return path, nil
		}
		link, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(link) {
			// Not filepath.Join: it would clean "sub/../x" to "x", where the
			// kernel, and with it the policy's resolve, goes up from
			// wherever the link sub leads.
			dir, _ := filepath.Split(path)
			link = dir + link
		}
		path = link
	}
	return "", &os.PathError{Op: "write", Path: path, Err: syscall.ELOOP}
}
