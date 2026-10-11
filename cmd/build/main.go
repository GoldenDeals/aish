// Command build generates a main.go that blank-imports the given modules and builds the binary,
// the way xcaddy does for caddy. Arguments after -- go to go build as is:
//
//	go run ./cmd/build -with github.com/you/llm@v1.2.0 -with github.com/you/ui=../ui -o aish -- -trimpath -ldflags="-s -w"
package main

import (
	"bytes"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

const corePath = "github.com/GoldenDeals/aish-core"

//go:embed main.go.tmpl
var mainSrc string

var mainTmpl = template.Must(template.New("main.go").Parse(mainSrc))

// dep is one -with value: path[@version] or path=local/dir; a path without a domain is under modules/ of aish-core.
type dep struct{ path, version, replace string }

func parseDep(s string) (dep, error) {
	var d dep
	rest, replace, _ := strings.Cut(s, "=")
	d.path, d.version, _ = strings.Cut(rest, "@")
	d.replace = replace
	switch {
	case d.path == "":
		return d, fmt.Errorf("-with %q: empty module path", s)
	case d.version != "" && d.replace != "":
		return d, fmt.Errorf("-with %q: use either @version or =local/dir", s)
	}
	// Module paths start with a domain; without a dot it's a module of this repo: llm/anthropic, frontend/tui/core.
	if first, _, _ := strings.Cut(d.path, "/"); !strings.Contains(first, ".") {
		d.path = corePath + "/modules/" + d.path
	}
	return d, nil
}

func main() {
	var deps []dep
	flag.Func("with", "module to include: path[@version] or path=local/dir; repeatable", func(s string) error {
		d, err := parseDep(s)
		deps = append(deps, d)
		return err
	})
	out := flag.String("o", "aish", "output binary")
	core := flag.String("core", "", "local aish-core checkout (default: the module in the current directory)")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "usage: build -with module... [-o file] [-core dir] [-- go build flags]")
		flag.PrintDefaults()
	}
	flag.Parse()

	if err := build(deps, *core, *out, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		os.Exit(1)
	}
}

// build makes the binary; goflags go to go build before its own -o, so they can't redirect the output.
func build(deps []dep, core, out string, goflags []string) (err error) {
	if len(deps) == 0 {
		return errors.New("no modules: pass at least one -with")
	}
	// shortcut: core only via a local replace, add core@version once it's published
	if core == "" {
		if core, err = coreDir(); err != nil {
			return err
		}
	}
	if core, err = filepath.Abs(core); err != nil {
		return err
	}
	if out, err = filepath.Abs(out); err != nil {
		return err
	}

	dir, err := os.MkdirTemp("", "aish-build-")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w (sources kept in %s)", err, dir)
			return
		}
		os.RemoveAll(dir)
	}()

	var src bytes.Buffer
	var paths []string
	for _, d := range deps {
		paths = append(paths, d.path)
	}
	if err := mainTmpl.Execute(&src, paths); err != nil {
		return err
	}
	code, err := format.Source(src.Bytes())
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), code, 0o644); err != nil {
		return err
	}

	cmds := [][]string{{"mod", "init", "aish"}, {"mod", "edit", "-replace", corePath + "=" + core}}
	for _, d := range deps {
		if d.replace != "" {
			abs, err := filepath.Abs(d.replace)
			if err != nil {
				return err
			}
			cmds = append(cmds, []string{"mod", "edit", "-replace", d.path + "=" + abs})
		}
	}
	for _, d := range deps { // after every replace, so go get sees them
		if d.version != "" {
			cmds = append(cmds, []string{"get", d.path + "@" + d.version})
		}
	}
	gobuild := append(append([]string{"build"}, goflags...), "-o", out, ".")
	cmds = append(cmds, []string{"mod", "tidy"}, gobuild)
	for _, args := range cmds {
		if err := gocmd(dir, args...); err != nil {
			return err
		}
	}
	return nil
}

// coreDir returns the root of the aish-core checkout the current directory belongs to.
func coreDir() (string, error) {
	b, err := exec.Command("go", "list", "-m", "-f", "{{.Path}} {{.Dir}}").Output()
	if err != nil {
		return "", fmt.Errorf("go list -m: %w", err)
	}
	path, dir, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	if path != corePath {
		return "", fmt.Errorf("current module is %q, not %s: run from an aish-core checkout or pass -core", path, corePath)
	}
	return dir, nil
}

func gocmd(dir string, args ...string) error {
	cmd := exec.Command("go", args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "GOWORK=off") // a go.work around the user must not leak into the build
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
