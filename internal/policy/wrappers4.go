package policy

import (
	"path/filepath"
	"slices"
	"strings"
)

// composeMark is the option composeRead marks the subcommand of compose
// with, at its word: the options after it are those of compose.
const composeMark = "compose"

func composed(o option) bool { return o.name == composeMark }

// The options of compose as Docker Compose 5 and nerdctl compose read them:
// composeOpts those of the program, found as cobra's Traverse finds them
// (see cobraSub), composeBools those of them that take no value, and those
// of exec, run and attach, which read the ones of nerdctl compose too
// (composeOwn).
var (
	composeOwn    = " env-file: file: ipfs-address: profile: project-directory: project-name:"
	composeOpts   = getopt{short: "f:hp:v", long: "all-resources ansi: compatibility dry-run env-file: file: help insecure-registry: ipfs-address: no-ansi parallel: profile: progress: project-directory: project-name: verbose version workdir:"}
	composeBools  = []string{"all-resources", "compatibility", "dry-run", "no-ansi", "verbose", "v", "version", "h", "help"}
	composeExec   = getopt{short: "+de:f:hip:tTu:w:", long: "detach dry-run env: help index: interactive no-TTY no-tty privileged tty user: workdir:" + composeOwn}
	composeRun    = getopt{short: "+de:f:hil:p:PqtTu:v:w:", long: "build cap-add: cap-drop: detach dry-run entrypoint: env: env-from-file: help interactive label: labels: name: no-build no-color no-deps no-log-prefix no-TTY no-tty publish: pull: quiet quiet-build quiet-pull remove-orphans rm service-ports tty use-aliases user: volume: volumes: workdir:" + composeOwn}
	composeAttach = getopt{short: "f:hp:", long: "detach-keys: dry-run help index: no-stdin sig-proxy" + composeOwn}
)

// composeTables are all the options of compose, for fixed.
func composeTables() getopt {
	return getopt{
		short: composeRun.short + composeExec.short + composeAttach.short + composeOpts.short,
		long:  composeRun.long + " " + composeExec.long + " " + composeAttach.long + " " + composeOpts.long,
	}
}

// composeWrapper is docker-compose, and podman-compose, which reads its
// words as it does; docker, podman and nerdctl read those after their
// compose with it.
var composeWrapper = wrapper{
	opts: composeTables(),
	reads: func(args []string) ([]option, int) {
		r := composeRead(args, 0)
		return r.opts, r.cmd
	},
	prog: func(opts []option, static []bool) ([]string, bool) { return composeEntry(opts, static) },
	bare: true, attach: []string{"i", "interactive"},
	check: func(p *parser, opts []option, args []string, static []bool, _ int) []string {
		if r := composeRead(args, 0); r.entry != nil && !static[r.image] {
			p.mark(dynComputed)
		}
		return composeCheck(p, opts, args, static)
	},
}

// composeRead reads the words of compose from i on: the options of the
// program, the subcommand, and of exec and run their options, the service
// and the command after it, which run without one takes from the compose
// file. The first option is composeMark, at the word of the subcommand.
// run with no command and attach hand stdin to the service, which may be
// a shell: they are marked as -i, unless run -d or attach --no-stdin.
func composeRead(args []string, i int) boxRead {
	s := i + cobraSub(args[i:], composeBools, "exec", "run", "attach")
	own, _, _ := readAll(composeOpts, args[:s], i)
	r := boxRead{opts: append([]option{{name: composeMark, word: s}}, own...), cmd: len(args), image: -1}
	if s == len(args) || has(own, "h", "help", "v", "version") {
		return r
	}
	switch args[s] {
	case "exec", "run":
		g := composeExec
		if args[s] == "run" {
			g = composeRun
		}
		more, svc := readFrom(g, args, s+1)
		r.opts = append(r.opts, more...)
		if has(more, "h", "help") || svc == len(args) {
			return r
		}
		r.image, r.cmd = svc, svc+1
		if args[s] != "run" {
			return r
		}
		if r.entry, _ = composeEntry(more, nil); r.entry != nil {
			r.cmd = svc
		}
		if r.cmd == len(args) && !has(more, "d", "detach") {
			r.opts = append(r.opts, option{name: "interactive", word: s})
		}
	case "attach":
		more, _, _ := readAll(composeAttach, args, s+1)
		r.opts = append(r.opts, more...)
		if !has(more, "no-stdin", "h", "help") {
			r.opts = append(r.opts, option{name: "interactive", word: s})
		}
	}
	return r
}

// composeEntry is what --entrypoint of compose run runs in place of the
// service: its value split at blanks, as go-shellwords splits it, nil for
// none or a value of no word, and whether it is static by static, nil for
// none. A value with quotes, an escape or one of ;&|<>, where go-shellwords
// splits otherwise or stops, and more than one --entrypoint, all of which
// nerdctl compose takes, is the whole value made at run time.
func composeEntry(opts []option, static []bool) ([]string, bool) {
	var es []option
	for _, o := range opts {
		if o.name == "entrypoint" {
			es = append(es, o)
		}
	}
	switch {
	case len(es) == 0:
		return nil, false
	case len(es) > 1, strings.ContainsAny(es[0].value, `'"\;&|<>`):
		return []string{es[0].value}, false
	}
	words := strings.FieldsFunc(es[0].value, func(r rune) bool { return r == ' ' || r == '\t' || r == '\r' || r == '\n' })
	if len(words) == 0 {
		return nil, false
	}
	return words, static != nil && static[es[0].word]
}

// composeCheck marks what compose runs besides its words: the files of -f,
// --file and --env-file (see composeFiles); the variables of -e and --env,
// those of --env-from-file, which may be any, and an option the tables do
// not hold. opts start with composeMark.
func composeCheck(p *parser, opts []option, args []string, static []bool) []string {
	s := opts[0].word
	sub := ""
	if s < len(args) {
		sub = args[s]
	}
	table := map[string]getopt{"exec": composeExec, "run": composeRun, "attach": composeAttach}[sub]
	var own, more []option
	for _, o := range opts[1:] {
		switch {
		case o.word < s:
			own = append(own, o)
		case o.word > s:
			more = append(more, o)
		}
	}
	p.strange(composeOpts, own)
	p.strange(table, more)
	for _, o := range more {
		switch o.name {
		case "e", "env":
			p.setenv(o.value, static[o.word])
		case "env-from-file":
			p.mark(dynComputed)
		}
	}
	if sub != "" && sub != "help" && sub != "version" && sub != "ls" && !has(own, "h", "help", "v", "version") && !has(more, "h", "help") {
		composeFiles(p, opts[1:], static)
	}
	return nil
}

// composeFiles marks the files a compose that loads its project reads
// besides those of its own: the compose files of -f and --file, which hold
// the commands of the services, their hooks and builds, and the env files
// of --env-file, whose variables fill them in and may name other compose
// files, as source; "-" reads a compose file from stdin, and a file made at
// run time may be either. Its own are those it finds in the directory it
// runs in or above it, and their .env, which it reads with no option:
// they are of the project, as the Makefile of make is, and are not
// marked, nor when -f or --env-file names them in that directory.
func composeFiles(p *parser, opts []option, static []bool) {
	for _, o := range opts {
		env := o.name == "env-file"
		switch {
		case o.name != "f" && o.name != "file" && !env:
		case !static[o.word]:
			p.mark(dynComputed)
		case !env && stdinFile(o.value):
			p.mark(dynStdin)
		case env && filepath.Clean(o.value) == ".env", !env && composeDefault(o.value):
		default:
			p.mark(dynSource)
		}
	}
}

// composeDefault tells whether a compose file is one compose finds with no
// -f in the directory it runs in: compose.yaml, docker-compose.yml and the
// .override of them.
func composeDefault(name string) bool {
	base := strings.TrimPrefix(filepath.Clean(name), "docker-")
	for _, ext := range []string{".yaml", ".yml"} {
		if stem, ok := strings.CutSuffix(base, ext); ok {
			return stem == "compose" || stem == "compose.override"
		}
	}
	return false
}

// nerdctlGlobal are the options of nerdctl its subcommands read too,
// nerdctlCLI is nerdctl 2, which finds its subcommand as cobra does: it runs
// the CNI plugins of --cni-path, by the names the configs of
// --cni-netconfpath give, and the hooks of the CDI specs of --cdi-spec-dirs;
// --init-binary is a program the container runs, --runtime one of the
// engine, and --entrypoint takes a word each time, all of them the
// entrypoint.
var (
	nerdctlGlobal = " address: bridge-ip: cdi-spec-dirs: cgroup-manager: cni-netconfpath: cni-path: data-root: debug debug-full experimental global-dns: global-dns-opts: global-dns-search: help host: host-gateway-ip: hosts-dir: insecure-registry kube-hide-dupe log-file: namespace: selinux-enabled snapshotter: storage-driver: userns-remap:"
	nerdctlCLI    = boxCLI{
		global: getopt{short: "a:hH:n:v", long: "version" + nerdctlGlobal},
		cobra:  true,
		bools:  []string{"debug", "debug-full", "experimental", "insecure-registry", "kube-hide-dupe", "selinux-enabled", "h", "help", "v", "version"},
		exec:   getopt{short: "+de:hH:in:tu:w:", long: "detach env: env-file: interactive privileged tty user: workdir:" + nerdctlGlobal},
		run: getopt{
			short: "+a:de:h:H:il:m:n:p:Pqtu:v:w:",
			long:  "add-host: annotation: attach: blkio-weight: blkio-weight-device: cap-add: cap-drop: cgroup-conf: cgroup-parent: cgroupns: cidfile: cosign-certificate-identity: cosign-certificate-identity-regexp: cosign-certificate-oidc-issuer: cosign-certificate-oidc-issuer-regexp: cosign-key: cpu-period: cpu-quota: cpu-rt-period: cpu-rt-runtime: cpu-shares: cpus: cpuset-cpus: cpuset-mems: detach detach-keys: device: device-read-bps: device-read-iops: device-write-bps: device-write-iops: dns: dns-opt: dns-option: dns-search: domainname: entrypoint: env: env-file: expose: gpus: group-add: health-cmd: health-interval: health-retries: health-start-period: health-timeout: hostname: init init-binary: interactive ip: ip6: ipc: ipfs-address: isolation: kernel-memory: label: label-file: log-driver: log-opt: mac-address: memory: memory-reservation: memory-swap: memory-swappiness: mount: name: net: network: no-healthcheck oom-kill-disable oom-score-adj: pid: pidfile: pids-limit: platform: privileged publish: publish-all pull: quiet rdt-class: read-only restart: rm rootfs runtime: security-opt: shm-size: sig-proxy stop-signal: stop-timeout: sysctl: systemd: tmpfs: tty ulimit: umask: user: userns: uts: verify: volume: volumes-from: workdir:" + nerdctlGlobal,
		},
		attach:   getopt{short: "hH:n:", long: "detach-keys: no-stdin" + nerdctlGlobal},
		start:    getopt{short: "ahH:in:", long: "attach checkpoint: checkpoint-dir: detach-keys: interactive" + nerdctlGlobal},
		multi:    true,
		programs: []string{"init-binary", "runtime"},
		opaque:   []string{"cdi-spec-dirs", "cni-netconfpath", "cni-path"},
	}
)

// kubeSubs are the subcommands of kubectl and oc that run a command or hand
// stdin to one: kubectlArgs reads exec, kube the others.
var kubeSubs = []string{"exec", "run", "debug", "attach", "rsh"}

// The options of the subcommands of kubectl 1.36 and oc that kube reads,
// with those of the program, which they read too (see kubectlOpts).
var (
	kubeRun = getopt{
		short: kubectlOpts.short + "k:l:o:R",
		long:  kubectlOpts.long + " allow-missing-template-keys annotations: attach cascade:: command detach-keys: dry-run:: env: expose field-manager: force grace-period: image: image-pull-policy: kustomize: labels: leave-stdin-open output: override-type: overrides: port: privileged recursive restart: rm save-config show-managed-fields template: timeout: wait",
	}
	kubeDebug = getopt{
		short: kubectlOpts.short,
		long:  kubectlOpts.long + " arguments-only attach copy-to: custom: env: image: image-pull-policy: keep-annotations keep-init-containers keep-labels keep-liveness keep-readiness keep-startup replace same-node set-image: share-processes target:",
	}
	kubeAttach = getopt{short: kubectlOpts.short, long: kubectlOpts.long + " detach-keys:"}
	ocRsh      = getopt{short: "+" + kubectlOpts.short + "T", long: kubectlOpts.long + " no-tty shell:"}
	ocDebug    = getopt{
		short: kubectlOpts.short + "Ik:o:RT",
		long:  kubectlOpts.long + " allow-missing-template-keys as-root as-user: dry-run:: image: image-stream: keep-annotations keep-init-containers keep-labels keep-liveness keep-readiness keep-startup kustomize: no-headers no-stdin no-tty node-name: one-container output: preserve-pod recursive show-all show-labels show-managed-fields sort-by: template: to-namespace:",
	}
)

// kube is kubectl, or oc of OpenShift, which shares its subcommands and
// options and adds rsh and a debug of its own.
type kube struct{ oc bool }

// kubeWrapper is kube{oc} as a wrapper. Whatever it runs, the client loads
// its kubeconfig and kuberc, whose users run the programs of exec and whose
// aliases make other commands of it: those of the user, as ~/.bashrc is,
// unless the line names others (see check).
func kubeWrapper(oc bool) wrapper {
	k := kube{oc}
	return wrapper{
		opts:  kubectlOpts,
		reads: func(args []string) ([]option, int) { r := k.parse(args); return r.opts, r.cmd },
		none:  []string{"h", "help"}, bare: true, attach: []string{"i", "stdin"}, check: k.check,
	}
}

// kubeRead is what kube reads of a line: the options, the index of the
// command (len(args) for none), that of the subcommand (len(args) for
// none), the operands after it and the "--" that ended the options, -1 for
// none.
type kubeRead struct {
	opts       []option
	cmd, sub   int
	ops        []int
	dash       int
	subcommand string
}

// table is the options of the subcommand sub.
func (k kube) table(sub string) getopt {
	switch {
	case sub == "run":
		return kubeRun
	case sub == "debug" && k.oc:
		return ocDebug
	case sub == "debug":
		return kubeDebug
	case sub == "attach":
		return kubeAttach
	case sub == "rsh" && k.oc:
		return ocRsh
	}
	return kubectlOpts
}

// parse reads kubectl or oc: exec as kubectlArgs does; run NAME [COMMAND],
// whose operands after the name are the command, "--" or not; debug,
// whose command is after "--", and attach, which run none; oc rsh POD
// [COMMAND], whose options end at the pod, and which takes the pod from
// the file of -f; the shell of rsh and of oc debug, which run one with no
// command, reads stdin: it is marked as -i, unless oc debug -I.
func (k kube) parse(args []string) kubeRead {
	s := cobraSub(args, kubectlBools, kubeSubs...)
	r := kubeRead{cmd: len(args), sub: s, dash: -1}
	if s == len(args) {
		return r
	}
	r.subcommand = args[s]
	switch {
	case r.subcommand == "exec":
		r.opts, r.cmd = kubectlArgs(args)
		_, r.ops, r.dash = readAll(kubectlOpts, args, s+1)
		return r
	case r.subcommand == "rsh" && !k.oc, !slices.Contains(kubeSubs, r.subcommand):
		return r
	}
	g := k.table(r.subcommand)
	pre, _ := kubectlOpts.read(args[:s])
	var opts []option
	if r.subcommand == "rsh" {
		var pod int
		opts, pod = readFrom(g, args, s+1)
		r.cmd = pod
		if pod < len(args) && !has(opts, "f", "filename") {
			r.cmd = pod + 1
		}
	} else {
		opts, r.ops, r.dash = readAll(g, args, s+1)
	}
	r.opts = append(pre, opts...)
	if has(r.opts, "h", "help") {
		r.cmd = len(args)
		return r
	}
	switch r.subcommand {
	case "run":
		if len(r.ops) > 1 {
			r.cmd = r.ops[1]
		}
	case "debug":
		if r.dash >= 0 && r.dash+1 < len(args) {
			r.cmd = r.dash + 1
		}
		if k.oc && r.cmd == len(args) && !has(r.opts, "I", "no-stdin") {
			r.opts = append(r.opts, option{name: "stdin", word: s})
		}
	case "rsh":
		if r.cmd >= len(args) {
			r.cmd = len(args)
			r.opts = append(r.opts, option{name: "stdin", word: s})
		}
	}
	return r
}

// check marks what kube runs besides its words: a kubeconfig or kuberc
// other than the user's, named by --kubeconfig or --kuberc (--config of
// oc) wherever it is before "--", as rebind; an option the table does not
// hold, one among the words of the command and the "--" kubectl run takes
// out of them; the variables of --env and of
// the NAME=VALUE operands of oc debug; --overrides and --custom, which may
// give the container any command. It returns the --shell of oc rsh, which
// it runs with no command.
func (k kube) check(p *parser, opts []option, args []string, static []bool, cmd int) []string {
	r := k.parse(args)
	kubectlCheck(p, k.table(r.subcommand), opts, cmd)
	if r.dash > cmd {
		p.mark(dynComputed)
	}
	for _, a := range args {
		if a == "--" {
			break
		}
		// oc took the kubeconfig from --config before it had --kubeconfig.
		if name, _, _ := strings.Cut(a, "="); name == "--kubeconfig" || name == "--kuberc" || k.oc && name == "--config" {
			p.mark(dynRebind)
		}
	}
	var code []string
	for _, o := range opts {
		switch o.name {
		case "env":
			p.setenv(o.value, static[o.word])
		case "overrides", "custom":
			p.mark(dynComputed)
		case "shell":
			switch {
			case cmd < len(args):
			case static[o.word]:
				code = append(code, escaped([]string{o.value}))
			default:
				p.mark(dynComputed)
			}
		}
	}
	if k.oc && r.subcommand == "debug" {
		// The resources come first, then the variables, up to the "--".
		for _, i := range r.ops {
			if r.dash >= 0 && i > r.dash {
				break
			}
			if strings.Contains(args[i], "=") {
				p.setenv(args[i], static[i])
			}
		}
	}
	return code
}

// ephemeralArgs reads the words of distrobox-ephemeral from i on as its
// loop does: -n, -a, -ap, --init-hooks and --pre-init-hooks take the next
// word, and without one (or with an empty one) it loops for ever, marked
// "?"; -h and -V print; a word of none of its options is one for
// distrobox-create (see ephemeralCode); -e, --exec and "--" end them with
// the command, which is not returned: the words after them are joined and
// split again. An empty word ends them with none. With none it runs a
// login shell in the container, marked "ephemeral", for attach.
func ephemeralArgs(args []string, i int) ([]option, int) {
	var opts []option
	shell := []option{{name: "ephemeral"}}
	for ; i < len(args); i++ {
		switch a := args[i]; a {
		case "-h", "--help", "-V", "--version":
			return append(opts, option{name: strings.TrimLeft(a, "-"), word: i}), len(args)
		case "-r", "--root", "-v", "--verbose":
			opts = append(opts, option{name: strings.TrimLeft(a, "-"), word: i})
		case "-e", "--exec", "--":
			if i+1 == len(args) {
				return append(opts, shell...), len(args)
			}
			return append(opts, option{name: "e", word: i}), len(args)
		case "-n", "--name", "-a", "--additional-flags", "-ap", "--additional-packages", "--init-hooks", "--pre-init-hooks":
			if i+1 == len(args) || args[i+1] == "" {
				return append(opts, option{name: "?", word: i}), len(args)
			}
			opts = append(opts, option{name: strings.TrimLeft(a, "-"), value: args[i+1], word: i + 1})
			i++
		case "":
			return append(opts, shell...), len(args)
		default:
			opts = append(opts, option{name: "create", value: a, word: i})
		}
	}
	return append(opts, shell...), len(args)
}

// ephemeralCheck is the check of distrobox, and of distrobox-ephemeral,
// whose words begin at from: that of distroboxCheck, and the code of
// ephemeral (see ephemeralCode).
func ephemeralCheck(from int) func(p *parser, opts []option, args []string, static []bool, cmd int) []string {
	return func(p *parser, opts []option, args []string, static []bool, cmd int) []string {
		code := distroboxCheck(p, opts, args, static, cmd)
		if from > 0 && (len(args) == 0 || args[0] != "ephemeral") || has(opts, distroboxNone...) {
			return code
		}
		return append(code, ephemeralCode(p, opts, args, static)...)
	}
}

// ephemeralCode is the code distrobox-ephemeral runs: the line of
// distrobox-create it builds of its words and evals, with a word of none
// of its options as it is, the values of -a, -ap and the hooks in double
// quotes and the name bare; the hooks, which the container evals as it
// starts; and the command after -e, --exec or "--", whose words it joins
// and splits again, and globs, for distrobox-enter. What is made at run
// time is marked.
func ephemeralCode(p *parser, opts []option, args []string, static []bool) []string {
	var flags, pkgs, create []string
	hooks, pre, name := " ", " ", "distrobox-XXXXXXXXXX"
	verbose, root, known := false, false, true
	var code []string
	for _, o := range opts {
		switch o.name {
		case "a", "additional-flags":
			flags = append(flags, o.value)
		case "ap", "additional-packages":
			pkgs = append(pkgs, o.value)
		case "init-hooks", "pre-init-hooks":
			if o.name == "init-hooks" {
				hooks = o.value
			} else {
				pre = o.value
			}
			if static[o.word] {
				code = append(code, o.value)
			}
		case "n", "name":
			name = o.value
		case "v", "verbose":
			verbose = true
		case "r", "root":
			root = true
		case "create":
			create = append(create, o.value)
		case "e":
			code = append(code, ephemeralCommand(p, args[o.word+1:], static[o.word+1:])...)
			continue
		default:
			continue
		}
		known = known && static[o.word]
	}
	if !known {
		p.mark(dynComputed)
		return code
	}
	line := "distrobox-create"
	if flags != nil {
		line += ` --additional-flags " ` + strings.Join(flags, " ") + `"`
	}
	if pkgs != nil {
		line += ` --additional-packages " ` + strings.Join(pkgs, " ") + `"`
	}
	line += ` --init-hooks "` + hooks + `" --pre-init-hooks "` + pre + `"`
	if verbose {
		line += " --verbose"
	}
	if root {
		line += " --root"
	}
	for _, c := range create {
		line += " " + c
	}
	return append(code, line+" --yes --name "+name)
}

// ephemeralCommand is the command distrobox-ephemeral runs of words: they
// are joined with spaces and split again at blanks, and globbed. A word
// made at run time, a glob and no word at all are marked.
func ephemeralCommand(p *parser, words []string, static []bool) []string {
	fields := strings.FieldsFunc(strings.Join(words, " "), func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' })
	if slices.Contains(static, false) || len(fields) == 0 || slices.ContainsFunc(fields, func(f string) bool { return strings.ContainsAny(f, "*?[") }) {
		p.mark(dynComputed)
		return nil
	}
	return []string{escaped(fields)}
}

// distroboxSubCheck is the check of distrobox, by the program of its first
// word: distrobox-create, distrobox-assemble, or that of ephemeralCheck.
func distroboxSubCheck(p *parser, opts []option, args []string, static []bool, cmd int) []string {
	switch {
	case len(args) > 0 && args[0] == "create":
		return createCheck(p, opts, args, static, cmd)
	case len(args) > 0 && args[0] == "assemble":
		return assembleCheck(p, opts, args, static, cmd)
	}
	return ephemeralCheck(1)(p, opts, args, static, cmd)
}

// The options of distrobox-create 1.8: createValued take the next word,
// createFlags none.
var (
	createValued = []string{
		"-i", "--image", "-n", "--name", "--hostname", "-c", "--clone", "-H", "--home", "--volume", "--platform",
		"-a", "--additional-flags", "-ap", "--additional-packages", "--init-hooks", "--pre-init-hooks",
	}
	createFlags = []string{
		"-h", "--help", "-v", "--verbose", "-V", "--version", "--no-entry", "-d", "--dry-run", "-r", "--root",
		"--absolutely-disable-root-password-i-am-really-positively-sure", "-I", "--init", "--unshare-ipc",
		"--unshare-groups", "--unshare-netns", "--unshare-process", "--unshare-devsys", "--unshare-all",
		"-C", "--compatibility", "-p", "--pull", "--nvidia", "-Y", "--yes",
	}
)

// unknownOpt names an option a program of distrobox 1.8 fails on and a
// newer one may take, with a value too: it is read past and marked.
const unknownOpt = "-"

// createArgs reads the words of distrobox-create from i on as its loop
// does: an option is a word of its own, those of createValued take the
// next one, and without one (or with an empty one) it loops for ever,
// marked "?"; -h, -V and -C print and exit; a word of none of its options
// names the container, as -n does; "--" and an empty word end them. It
// runs no command of the user's.
func createArgs(args []string, i int) ([]option, int) {
	var opts []option
	for ; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--", a == "":
			return opts, len(args)
		case slices.Contains(createValued, a):
			if i+1 == len(args) || args[i+1] == "" {
				return append(opts, option{name: "?", word: i}), len(args)
			}
			opts = append(opts, option{name: strings.TrimLeft(a, "-"), value: args[i+1], word: i + 1})
			i++
		case slices.Contains(createFlags, a):
			name := strings.TrimLeft(a, "-")
			opts = append(opts, option{name: name, word: i})
			if slices.Contains([]string{"h", "help", "V", "version", "C", "compatibility"}, name) {
				return opts, len(args)
			}
		case strings.HasPrefix(a, "-"):
			opts = append(opts, option{name: unknownOpt, value: a, word: i})
		default:
			opts = append(opts, option{name: "n", value: a, word: i})
		}
	}
	return opts, len(args)
}

// createCheck is the check of distrobox-create, and of distrobox create:
// that of distroboxCheck, an option it does not know, and the code of
// createCode, unless it prints or loops.
func createCheck(p *parser, opts []option, args []string, static []bool, cmd int) []string {
	code := distroboxCheck(p, opts, args, static, cmd)
	if has(opts, unknownOpt) {
		p.mark(dynComputed)
	}
	if has(opts, distroboxNone...) || has(opts, "C", "compatibility") {
		return code
	}
	return append(code, createCode(p, opts, static)...)
}

// createCode is the code distrobox-create runs: the line of the container
// manager it builds of its options and evals here, with the image, the
// container of --clone, which becomes the image in lower case, the
// platform, the volumes and the flags of -a bare, the name, the hostname,
// the home, the pre-init hooks and the packages in double quotes and the
// init hooks in single quotes; and the hooks, which the container evals as
// it starts. A value made at run time may be any code in that line.
func createCode(p *parser, opts []option, static []bool) []string {
	line := ":"
	var code []string
	known := true
	for _, o := range opts {
		switch o.name {
		case "i", "image", "platform", "volume", "a", "additional-flags":
			line += " " + o.value
		case "c", "clone":
			line += " " + strings.ToLower(o.value)
		case "n", "name", "hostname", "H", "home", "ap", "additional-packages":
			line += ` "` + o.value + `"`
		case "pre-init-hooks":
			line += ` "` + o.value + `"`
		case "init-hooks":
			line += ` '` + o.value + `'`
		default:
			continue
		}
		known = known && static[o.word]
		if static[o.word] && (o.name == "init-hooks" || o.name == "pre-init-hooks") {
			code = append(code, o.value)
		}
	}
	if !known {
		p.mark(dynComputed)
		return code
	}
	return append(code, line)
}

// assembleArgs reads the words of distrobox-assemble from i on as its loop
// does: create and rm wherever they are, --file and -n take the next word,
// and without one (or with an empty one) it loops for ever, marked "?";
// -h and -V print and exit; a word of none of its options names the file,
// as --file does; "--" and an empty word end them. It runs no command of
// the user's.
func assembleArgs(args []string, i int) ([]option, int) {
	var opts []option
	for ; i < len(args); i++ {
		switch a := args[i]; a {
		case "--", "":
			return opts, len(args)
		case "create", "rm":
			opts = append(opts, option{name: a, word: i})
		case "--file", "-n", "--name":
			if i+1 == len(args) || args[i+1] == "" {
				return append(opts, option{name: "?", word: i}), len(args)
			}
			opts = append(opts, option{name: strings.TrimLeft(a, "-"), value: args[i+1], word: i + 1})
			i++
		case "-h", "--help", "-V", "--version":
			return append(opts, option{name: strings.TrimLeft(a, "-"), word: i}), len(args)
		case "-d", "--dry-run", "-v", "--verbose", "-R", "--replace":
			// Not marked: dry run or not, it evals the lines it builds.
		default:
			if strings.HasPrefix(a, "-") {
				opts = append(opts, option{name: unknownOpt, value: a, word: i})
			} else {
				opts = append(opts, option{name: "file", value: a, word: i})
			}
		}
	}
	return opts, len(args)
}

// assembleCheck marks the manifest distrobox-assemble create or rm reads,
// whose values make the distrobox-create and distrobox rm lines it evals,
// dry run or not: one the line names, a file or a URL it downloads, as
// source; its own, ./distrobox.ini, which it reads with no file, is of the
// project, as the Makefile of make is, and is not marked. A file made at
// run time and an option it does not know are marked.
func assembleCheck(p *parser, opts []option, _ []string, static []bool, _ int) []string {
	if has(opts, unknownOpt) {
		p.mark(dynComputed)
	}
	if has(opts, "h", "help", "V", "version", "?") || !has(opts, "create", "rm") {
		return nil
	}
	for _, o := range opts {
		switch {
		case o.name != "file":
		case !static[o.word]:
			p.mark(dynComputed)
		case filepath.Clean(o.value) != "distrobox.ini":
			p.mark(dynSource)
		}
	}
	return nil
}

// ipCheck marks the commands of ip -batch: read from stdin for "-", else
// from a file, which may change after the check, as source does; one made
// at run time may be either.
func ipCheck(p *parser, opts []option, _ []string, static []bool, _ int) []string {
	for _, o := range opts {
		switch {
		case o.name != "batch":
		case !static[o.word]:
			p.mark(dynComputed)
		case stdinFile(o.value):
			p.mark(dynStdin)
		default:
			p.mark(dynSource)
		}
	}
	return nil
}

// stdinFile tells whether a program that reads the file name reads stdin.
func stdinFile(name string) bool {
	return name == "-" || name == "/dev/stdin" || name == "/dev/fd/0" || name == "/proc/self/fd/0"
}
