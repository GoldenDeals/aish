package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/inebotov/aish/internal/config"
)

// trustCmd is aish trust [--revoke | --list]: the project file of this
// directory, as it is now, may run code from the repository, its hooks
// and tools. Typed as aish trust only: a trust command of the system
// (p11-kit's) would take the bare word.
func trustCmd(cfg config.Config, args []string) int {
	const trustUsage = "usage: aish trust [--revoke | --list]"
	revoke := false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--list":
		return trustList()
	case len(args) == 1 && args[0] == "--revoke":
		revoke = true
	default:
		return fail(errors.New(trustUsage))
	}
	cwd, _ := os.Getwd()
	_, path, err := config.Project(cfg, cwd)
	if path == "" {
		if err != nil {
			return fail(err)
		}
		return fail(errors.New("no " + config.ProjectFile + " here"))
	}
	if revoke {
		if err := config.Untrust(path); err != nil {
			return fail(err)
		}
		fmt.Printf("%s is not trusted: its hooks_dir and tools_dir are skipped\n", home(path))
		return 0
	}
	// A file aish refuses is no use trusted.
	if err != nil {
		return fail(err)
	}
	keys, err := config.CodeKeys(path)
	if err != nil {
		return fail(err)
	}
	if err := config.Trust(path); err != nil {
		return fail(err)
	}
	if len(keys) == 0 {
		fmt.Printf("%s is trusted; it sets nothing that runs code\n", home(path))
		return 0
	}
	fmt.Printf("%s is trusted until it or its hooks and tools change; these run code from the repository now:\n", home(path))
	for _, k := range keys {
		fmt.Println("  " + k)
	}
	return 0
}

// untrustedNote is the dim line, with its newline, for a command that
// loads the project's tools when cfg went without keys of the project
// file: a tool of the repository is then not there, and nothing else
// would say why. "" when cfg has all the file sets.
func untrustedNote(cfg config.Config, project string) string {
	if len(cfg.Untrusted) == 0 {
		return ""
	}
	return fmt.Sprintf("\x1b[2m%s: %s not trusted; aish trust\x1b[0m\n", home(project), strings.Join(cfg.Untrusted, ", "))
}

// trustList prints the files trusted, with those changed since, or their
// hooks and tools, marked: they are not trusted as they are now.
func trustList() int {
	all := config.TrustedFiles()
	if len(all) == 0 {
		fmt.Println("no trusted files")
		return 0
	}
	paths := make([]string, 0, len(all))
	for p := range all {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		var note string
		sum := config.Sum(p)
		_, err := os.Stat(p)
		switch {
		case sum == "" && err != nil:
			note = " \x1b[2m(gone)\x1b[0m"
		case sum == "":
			note = " \x1b[33m(it or its hooks and tools cannot be read, not trusted)\x1b[0m"
		case sum != all[p]:
			note = " \x1b[33m(changed since, not trusted: run aish trust there again)\x1b[0m"
		}
		fmt.Println(home(p) + note)
	}
	return 0
}
