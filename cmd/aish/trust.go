package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// trustCmd is aish trust [--revoke | --list]: the project file of this
// directory, as it is now, may run code from the repository, its hooks
// and tools. Typed as aish trust only: a trust command of the system
// (p11-kit's) would take the bare word. Inside aish the proxy trusts it,
// once the user said Yes to its question (trustByProxy); --revoke, which
// only takes trust back, and --list ask nothing.
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
	if !revoke && os.Getenv("AISH_SOCK") != "" {
		return trustByProxy()
	}
	if assistantAsks() {
		return fail(errors.New(policy.TrustReason))
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
	fmt.Print(trustedText(path, keys))
	return 0
}

// trustByProxy is aish trust inside aish: the proxy finds the project file
// of this directory and trusts it once the user said Yes to its question,
// which it reads from the keyboard. Code the assistant left for the shell
// to run at the prompt would trust it otherwise, past the policies and
// the guard. A proxy older than rpc trust is not got around by writing
// trusted.json here.
func trustByProxy() int {
	client, err := rpc.FromEnv()
	if err != nil {
		return fail(err)
	}
	cwd, _ := os.Getwd()
	var res rpc.Trusted
	interrupted, err := confirmCall(client.Path, rpc.MethodTrust, rpc.TrustParams{Cwd: cwd}, &res)
	switch {
	case olderProxy(err):
		return fail(errors.New("this shell's aish is older than the question of aish trust: restart aish to trust here"))
	case err != nil && interrupted:
		fail(errors.New("trust: interrupted; not trusted"))
		return 130
	case err != nil:
		return fail(err)
	}
	fmt.Print(trustedText(res.Path, res.Keys))
	// This shell's aish goes by the file as it read it, which an edit
	// since does not match: the trust is in the contents.
	fmt.Println("\x1b[2mif it changed since this shell's aish read it, aish apply-config puts it in force here\x1b[0m")
	return 0
}

// trustedText is what aish trust prints of path trusted, keys the lines
// of it that run code.
func trustedText(path string, keys []string) string {
	if len(keys) == 0 {
		return fmt.Sprintf("%s is trusted; it sets nothing that runs code\n", home(path))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s is trusted until it or its hooks and tools change; these run code from the repository now:\n", home(path))
	for _, k := range keys {
		b.WriteString("  " + k + "\n")
	}
	return b.String()
}

// assistantAsks tells whether a request of the assistant is in progress
// in this shell, so that the command is the assistant's: the policies
// deny it aish trust, but not through code they cannot see into, a
// script or python -c. A proxy that does not answer is taken for no: a
// command that unsets AISH_SOCK gets past this anyway.
func assistantAsks() bool {
	client, err := rpc.FromEnv()
	if err != nil {
		return false
	}
	var info rpc.Info
	return client.Call(rpc.MethodInfo, nil, &info) == nil && info.Asking
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
