package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"

	"github.com/BurntSushi/toml"
)

// ProjectFile is the name of a project's settings file, looked for from
// the request's directory up to the root of its git repository.
const ProjectFile = ".aish.toml"

// project holds the keys a repository may set: those that cost nothing
// but the user's own time. Where the API goes and what it is paid with
// stay in config.toml, out of the reach of a cloned repository. Pointers
// tell a key that is set from one that is not.
type project struct {
	MaxSteps       *int    `toml:"max_steps"`
	MaxOutputBytes *int    `toml:"max_output_bytes"`
	FoldLines      *int    `toml:"fold_lines"`
	Markdown       *bool   `toml:"markdown"`
	CodeStyle      *string `toml:"code_style"`
	PolicyDir      *string `toml:"policy_dir"`
	Policy         *Policy `toml:"policy"`
	ToolsDir       *string `toml:"tools_dir"`
	SystemPrompt   *string `toml:"system_prompt"`
}

// Project lays the project's settings over cfg: the ProjectFile nearest
// to cwd, up to the root of the git repository (the directory holding
// .git) or /; the home directory and what is above it are not a project,
// config.toml is for those. Returns the config and the file it took,
// "" when there is none. A key not allowed in a project, or unknown, is
// an error: the request must not run with settings other than the user
// reads in the file.
//
// system_prompt is appended to the global one. policy_dir and tools_dir
// are added after the global directories: PolicyDir and ToolsDir come
// back as lists in the form of PATH (filepath.ListSeparator), which the
// policy and tools packages take. Relative directories are taken from
// the file's own. The deny and ask lists of [policy] are added to the
// global ones, and of the two write_outside_home the stricter is kept.
func Project(cfg Config, cwd string) (Config, string, error) {
	path := findProject(cwd)
	if path == "" {
		return cfg, "", nil
	}
	var pr project
	md, err := toml.DecodeFile(path, &pr)
	if err == nil {
		err = forbidden(md)
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, "", nil
		}
		return cfg, path, fmt.Errorf("%s: %w", path, err)
	}
	dir := filepath.Dir(path)
	if pr.MaxSteps != nil {
		cfg.MaxSteps = *pr.MaxSteps
	}
	if pr.MaxOutputBytes != nil {
		cfg.MaxOutputBytes = *pr.MaxOutputBytes
	}
	if pr.FoldLines != nil {
		cfg.FoldLines = *pr.FoldLines
	}
	if pr.Markdown != nil {
		cfg.Markdown = *pr.Markdown
	}
	if pr.CodeStyle != nil {
		cfg.CodeStyle = *pr.CodeStyle
	}
	if pr.PolicyDir != nil {
		cfg.PolicyDir = addDir(cfg.PolicyDir, *pr.PolicyDir, dir)
	}
	if pr.Policy != nil {
		cfg.Policy = Policy{
			Deny:             slices.Concat(cfg.Policy.Deny, pr.Policy.Deny),
			Ask:              slices.Concat(cfg.Policy.Ask, pr.Policy.Ask),
			WriteOutsideHome: stricter(cfg.Policy.WriteOutsideHome, pr.Policy.WriteOutsideHome),
		}
	}
	if pr.ToolsDir != nil {
		cfg.ToolsDir = addDir(cfg.ToolsDir, *pr.ToolsDir, dir)
	}
	if pr.SystemPrompt != nil {
		if cfg.SystemPrompt != "" && *pr.SystemPrompt != "" {
			cfg.SystemPrompt += "\n\n"
		}
		cfg.SystemPrompt += *pr.SystemPrompt
	}
	if err := cfg.check(); err != nil {
		return cfg, path, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, path, nil
}

// findProject is the ProjectFile for cwd, or "". It stops at the first
// directory with a .git (the repository's root: a file in a parent
// repository is someone else's), at the home directory, and at /.
func findProject(cwd string) string {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return ""
	}
	home, _ := os.UserHomeDir()
	for {
		if home != "" && dir == home {
			return ""
		}
		path := filepath.Join(dir, ProjectFile)
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path
		}
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// forbidden names the first undecoded key that config.toml would take: a
// project must not set it. Keys neither takes are unknown, as in Load.
func forbidden(md toml.MetaData) error {
	global := map[string]bool{}
	t := reflect.TypeOf(Config{})
	for i := range t.NumField() {
		if tag := t.Field(i).Tag.Get("toml"); tag != "" && tag != "-" {
			global[tag] = true
		}
	}
	for _, k := range md.Undecoded() {
		if global[k.String()] {
			return fmt.Errorf("key %s is not allowed in a project config: set it in config.toml", strconv.Quote(k.String()))
		}
	}
	return unknown(md)
}

// stricter is the stricter of two write_outside_home values. A value that
// is none of them wins, so that check rejects it.
func stricter(a, b string) string {
	rank := func(v string) int {
		switch v {
		case "", "allow":
			return 0
		case "ask":
			return 1
		case "deny":
			return 2
		}
		return 3
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// addDir appends dir, relative to base unless absolute or ~/, to the
// list in have.
func addDir(have, dir, base string) string {
	dir = expand(dir)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(base, dir)
	}
	if have == "" {
		return dir
	}
	return have + string(filepath.ListSeparator) + dir
}
