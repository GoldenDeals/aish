package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
)

// <id>.info keeps what List found in a session's journal and state, with
// each file as List saw it: size, mtime and inode. List reads a file again
// only when it is not so any more, so `aish resume` reads the journals of
// the sessions used since, not of every one.
//
// List writes it, not Append: a summary written with a request would be
// stale with the next entry, and the entries of a request are most of a
// journal. The check that finds it stale finds a journal changed by
// anything else too (another aish, an editor, an aish from before the
// file), and such a journal is read the way every one was before.

// infoVersion is bumped when what the file holds, or how it is found,
// changes: a file of another version is stale.
const infoVersion = 1

func infoPath(dir, id string) string { return filepath.Join(dir, id+".info") }

// stamp tells a file apart from what it was: a write changes its size or
// mtime, a SaveState its inode as well.
type stamp struct {
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // ns
	Ino   uint64 `json:"ino"`
}

// absent is the stamp of a file that is not there.
var absent = stamp{Size: -1}

func stampOf(fi os.FileInfo) stamp {
	s := stamp{Size: fi.Size(), MTime: fi.ModTime().UnixNano()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		s.Ino = uint64(st.Ino)
	}
	return s
}

// infoFile is <id>.info. A part whose stamp is nil was not read whole and
// is read again.
type infoFile struct {
	Version  int    `json:"v"`
	Journal  *stamp `json:"journal,omitempty"`
	Last     string `json:"last,omitempty"`
	Requests int    `json:"requests,omitempty"`
	State    *stamp `json:"state,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	Profile  string `json:"profile,omitempty"`
	TopLevel bool   `json:"top_level,omitempty"`
	Model    string `json:"model,omitempty"`
}

// summarize fills in what info has from the session's journal and state:
// from <id>.info for a file as it saw it, else from the file, and keeps
// what it read there for the next List.
func summarize(dir, id string, info *Info) {
	journal := filepath.Join(dir, id+".jsonl")
	fi, err := os.Stat(journal)
	if err != nil {
		return // removed since the glob
	}
	info.Modified = fi.ModTime()
	c := readInfo(dir, id)
	changed := false
	// Each file is stamped before it is read: one written meanwhile
	// differs from its stamp the next time.
	if js := stampOf(fi); c.Journal == nil || *c.Journal != js {
		last, n, err := requests(journal)
		c.Journal, c.Last, c.Requests, changed = nil, last, n, true
		if err == nil {
			c.Journal = &js
		}
	}
	info.Last, info.Requests = c.Last, c.Requests

	ss := absent
	fi, serr := os.Stat(statePath(dir, id))
	switch {
	case serr == nil:
		ss = stampOf(fi)
	case os.IsNotExist(serr):
		serr = nil
	}
	if c.State == nil || serr != nil || *c.State != ss {
		st, err := LoadState(dir, id)
		if err != nil {
			st = Saved{}
		}
		c.State, changed = nil, true
		c.Cwd, c.Profile, c.TopLevel, c.Model = st.Shell.Cwd, st.Profile, st.TopLevel, st.Model
		if err == nil && serr == nil {
			c.State = &ss
		}
	}
	info.Cwd, info.Profile, info.TopLevel, info.Model = c.Cwd, c.Profile, c.TopLevel, c.Model

	if changed {
		writeInfo(dir, id, c)
	}
}

// readInfo is the session's <id>.info, nothing known when there is none
// or it is of another version.
func readInfo(dir, id string) infoFile {
	var c infoFile
	b, err := os.ReadFile(infoPath(dir, id))
	if err != nil || json.Unmarshal(b, &c) != nil || c.Version != infoVersion {
		return infoFile{Version: infoVersion}
	}
	return c
}

// writeInfo puts c in <id>.info whole or not at all, as best it can: List
// reads the files when it cannot. It holds the last request, private to
// the user like the journal.
func writeInfo(dir, id string, c infoFile) {
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	path := infoPath(dir, id)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil || os.Rename(tmp, path) != nil {
		os.Remove(tmp)
		return
	}
	// A Remove meanwhile took the journal before the .info: the one put
	// back here would outlive the session.
	if _, err := os.Stat(filepath.Join(dir, id+".jsonl")); os.IsNotExist(err) {
		os.Remove(path)
	}
}
