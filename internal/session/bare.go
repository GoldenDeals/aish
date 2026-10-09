package session

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// A bare session has entries, yet none to come back to: a `&NAME` at the
// prompt of a new session puts it on disk with what its subagent spent
// (KindUsage), and a Ctrl+L after it adds a clear. List and Latest leave
// it out, so that `aish resume --all` and `aish --resume` do not offer a
// session with nothing in it. Its journal stays on disk all the same:
// aish stats counts that spend, Prune takes it with the rest. One the user
// named is listed: they know of it, and CheckName keeps its name taken.
//
// A bare session is saved by its first entry, like any: put off till an
// entry of another kind, what it spent would be lost to aish stats when
// the shell exits, or leaves it, before one.

// bareKinds are the kinds of entry that leave a session nothing to
// resume: the model is sent none of them.
var bareKinds = []string{KindUsage, KindClear}

// bareLine tells by its start whether a journal line is one of bareKinds.
// A line Append did not write, with its kind elsewhere, is taken for
// another: the session is listed as it was before.
func bareLine(line []byte) bool {
	rest, ok := bytes.CutPrefix(line, []byte(`{"kind":"`))
	if !ok {
		return false
	}
	for _, k := range bareKinds {
		if bytes.HasPrefix(rest, []byte(k+`"`)) {
			return true
		}
	}
	return false
}

// bare tells whether the journal at path is a bare session's. It reads
// up to the first entry of another kind, the first line of most journals;
// those of bare sessions are short. An empty journal is not bare: Save
// put it there before its first entry.
func bare(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	found := false
	for {
		line, err := br.ReadSlice('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			if !bareLine(line) {
				return false, nil
			}
			found = true
		}
		// The start told the line's kind: the rest is read past.
		for errors.Is(err, bufio.ErrBufferFull) {
			_, err = br.ReadSlice('\n')
		}
		switch {
		case errors.Is(err, io.EOF):
			return found, nil
		case err != nil:
			return false, err
		}
	}
}

// hidden tells whether List and Latest leave out session id of dir: it is
// bare and the user has not named it. A journal that cannot be read is
// not hidden: it is listed, or opened, as it was before.
func hidden(dir, id string) bool {
	b, err := bare(filepath.Join(dir, id+".jsonl"))
	return err == nil && b && readName(filepath.Join(dir, id+".name")) == ""
}
