package config

import (
	"bytes"
	"errors"
	"io/fs"
	"maps"
	"os"
	"reflect"
	"slices"
	"sync"

	"github.com/BurntSushi/toml"
)

// Snapshot is the config files as the proxy took them: config.toml as aish
// started or `aish apply-config` last read it, and the project file of each
// directory a request came from, as the first request there since read it.
// Requests go by the snapshot, not by the disk: an edit is in force once
// the user applies it, all of it at once, not one key from the next
// request and another only after a restart. A snapshot does not change
// once taken but for the project files it reads the first time it is
// asked about a directory.
//
// Trust is the exception. The keys of a project file that run code hold
// only while the file on disk is still the one read and is trusted as it
// is, its hooks and tools too: an edit, a git pull say, turns them off at
// once, as Project does, and `aish trust --revoke` too.
type Snapshot struct {
	path string
	cfg  read // config.toml

	mu    sync.Mutex
	dirs  map[string]string // a request's directory → its project file, "" if none
	files map[string]read   // a project file → its contents
}

// read is what reading a file gave.
type read struct {
	data []byte
	err  error
}

// NewSnapshot reads config.toml, or the file $AISH_CONFIG names, now. An
// error reading it is kept: LoadEnv and LoadProfile return it, as Load
// does.
func NewSnapshot() *Snapshot {
	path := configFile()
	data, err := os.ReadFile(path)
	return &Snapshot{path: path, cfg: read{data, err}, dirs: map[string]string{}, files: map[string]read{}}
}

// Path is the config.toml the snapshot is of.
func (s *Snapshot) Path() string { return s.path }

// LoadEnv is the package's LoadEnv of the snapshot: the environment, getenv
// the shell's, is read anew, the file is not.
func (s *Snapshot) LoadEnv(getenv func(string) string) (Config, error) {
	return parse(s.path, s.cfg.data, s.cfg.err, nil, getenv)
}

// LoadProfile is the package's LoadProfile of the snapshot.
func (s *Snapshot) LoadProfile(name string) (Config, error) {
	return parse(s.path, s.cfg.data, s.cfg.err, &name, os.Getenv)
}

// Project is the package's Project of the snapshot: which project file is
// cwd's, and what is in it, is read the first time cwd is asked about and
// kept, so that a file new there, or edited, is in force with the next
// snapshot. Whether its code holds is not kept (see Snapshot).
func (s *Snapshot) Project(cfg Config, cwd string) (Config, string, error) {
	path, f := s.project(cwd)
	if path == "" {
		return cfg, "", nil
	}
	return layProject(cfg, path, f.data, f.err, func() bool {
		return f.err == nil && onDisk(path).same(f) && trusted(path, f.data)
	})
}

// project is the project file of cwd and its contents, read now if cwd was
// not asked about before.
func (s *Snapshot) project(cwd string) (string, read) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, ok := s.dirs[cwd]
	if !ok {
		path = findProject(cwd)
		s.dirs[cwd] = path
	}
	if path == "" {
		return "", read{}
	}
	f, ok := s.files[path]
	if !ok {
		data, err := os.ReadFile(path)
		f = read{data, err}
		s.files[path] = f
	}
	return path, f
}

// Changed tells how config.toml and the project file of cwd are on disk
// where they differ from the snapshot, by a key for each: the proxy says
// once that an edit waits to be applied, and again for the next edit. A
// directory not asked about has no project file to differ.
func (s *Snapshot) Changed(cwd string) []string {
	var keys []string
	if now := onDisk(s.path); !now.same(s.cfg) {
		keys = append(keys, now.key(s.path))
	}
	s.mu.Lock()
	path, asked := s.dirs[cwd]
	f := s.files[path]
	s.mu.Unlock()
	if !asked {
		return keys
	}
	if found := findProject(cwd); found != path {
		keys = append(keys, cwd+"\x00"+found)
	} else if now := onDisk(path); path != "" && !now.same(f) {
		keys = append(keys, now.key(path))
	}
	return keys
}

// Stale names the files that differ on disk from the snapshot: config.toml,
// and the project files of the directories asked about, edited, gone, or
// another one nearer to the directory now.
func (s *Snapshot) Stale() []string {
	var names []string
	if !onDisk(s.path).same(s.cfg) {
		names = append(names, s.path)
	}
	s.mu.Lock()
	dirs, files := maps.Clone(s.dirs), maps.Clone(s.files)
	s.mu.Unlock()
	for cwd, path := range dirs {
		if found := findProject(cwd); found != path && found != "" {
			names = append(names, found)
		}
	}
	for path, f := range files {
		if !onDisk(path).same(f) {
			names = append(names, path)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// Keys are the keys of config.toml whose values differ in next, by their
// dotted paths (profiles.work.model); a table one of them has and the
// other has not is named itself. A file that does not decode has no keys.
func (s *Snapshot) Keys(next *Snapshot) []string {
	var keys []string
	diffKeys("", s.cfg.table(), next.cfg.table(), &keys)
	return keys
}

func (r read) table() map[string]any {
	m := map[string]any{}
	if r.err == nil {
		if _, err := toml.Decode(string(r.data), &m); err != nil {
			return map[string]any{}
		}
	}
	return m
}

func diffKeys(prefix string, a, b map[string]any, keys *[]string) {
	names := slices.Sorted(maps.Keys(a))
	for k := range b {
		if _, ok := a[k]; !ok {
			names = append(names, k)
		}
	}
	slices.Sort(names)
	for _, k := range names {
		va, inA := a[k]
		vb, inB := b[k]
		ta, tableA := va.(map[string]any)
		tb, tableB := vb.(map[string]any)
		switch {
		case inA && inB && tableA && tableB:
			diffKeys(prefix+k+".", ta, tb, keys)
		case inA != inB || !reflect.DeepEqual(va, vb):
			*keys = append(*keys, prefix+k)
		}
	}
}

// onDisk reads path as it is now. Not a FIFO, which would keep the read
// waiting for a writer with every request.
func onDisk(path string) read {
	data, err := readRegular(path)
	return read{data, err}
}

// same tells whether r and o read the same: the same contents, or no file
// either time, or the same error.
func (r read) same(o read) bool {
	switch {
	case r.err == nil && o.err == nil:
		return bytes.Equal(r.data, o.data)
	case r.err == nil || o.err == nil:
		return false
	case errors.Is(r.err, fs.ErrNotExist) && errors.Is(o.err, fs.ErrNotExist):
		return true
	}
	return r.err.Error() == o.err.Error()
}

// key tells what reading path gave apart from what it gives another time.
func (r read) key(path string) string {
	if r.err != nil {
		return path + "\x00" + r.err.Error()
	}
	return path + "\x00" + sha(r.data)
}
