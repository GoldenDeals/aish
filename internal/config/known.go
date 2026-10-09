package config

// Known is Project of the snapshot as far as it has read the files, with
// nothing read now and nothing kept: the project file of cwd as a request
// from there would take it, if the snapshot read that file for cwd or for
// another directory of the project; otherwise cfg as it is. The status at
// the prompt sizes the context by it after every command: reading a file
// there would put it in the snapshot before the request that goes by it,
// and an edit made in between would wait for `aish apply-config`. Trust
// is not checked, and the keys that need it are left out.
func (s *Snapshot) Known(cfg Config, cwd string) Config {
	s.mu.Lock()
	path, asked := s.dirs[cwd]
	s.mu.Unlock()
	if !asked {
		path = findProject(cwd)
	}
	if path == "" {
		return cfg
	}
	s.mu.Lock()
	f, read := s.files[path]
	s.mu.Unlock()
	if !read {
		return cfg
	}
	c, _, err := layProject(cfg, path, f.data, f.err, func() bool { return false })
	if err != nil {
		return cfg // a request from there fails on it
	}
	return c
}
