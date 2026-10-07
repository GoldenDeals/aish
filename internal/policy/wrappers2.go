package policy

import (
	"maps"
	"slices"
	"strings"
)

func init() {
	maps.Copy(wrappers, moreWrappers)
	// Here, not in the table: xargsFills walks the table.
	x := wrappers["xargs"]
	x.fills = xargsFills
	wrappers["xargs"] = x
}

// moreWrappers are the wrappers of packages other than those of wrappers,
// read as their sources read their words: util-linux 2.42 (setpriv,
// unshare, nsenter, taskset, chrt, prlimit, setarch, uclampset, choom),
// systemd 261 (systemd-run, run0, systemd-inhibit), polkit 127, strace 6,
// ltrace 0.7, valgrind 3, gdb 17, firejail 0.9, fakeroot 1.37, proxychains-ng
// 4, Expect's unbuffer, numactl 2, libcgroup 3, torsocks 2, glibc's
// catchsegv, runit's chpst and the daemontools it stands for, gosu, su-exec
// and caffeinate of macOS.
var moreWrappers = map[string]wrapper{
	"caffeinate": {opts: getopt{short: "+mdhisut:w:"}, none: []string{"h"}},
	"catchsegv":  {opts: getopt{short: "+", long: "help version"}, none: []string{"help", "version"}},
	"cgexec":     {opts: getopt{short: "+g:shbr", long: "sticky help"}, none: []string{"h", "help"}},
	"choom":      {opts: getopt{short: "hn:p:V", long: "adjust: pid: help version"}, none: []string{"p", "pid", "h", "help", "V", "version"}},
	"chpst":      {opts: chpstOpts, chdir: []string{"/", "C"}, none: []string{"V"}, check: chpstCheck},
	"chrt":       {opts: chrtOpts, reads: chrtArgs, none: []string{"p", "pid", "m", "max", "h", "help", "V", "version"}},
	"envdir":     {reads: noOpts(1), check: envdirCheck},
	"envuidgid":  {reads: noOpts(1)},
	"fakeroot": {
		opts: getopt{short: "+l:f:i:s:ub:vh", long: "lib: faked: unknown-is-real fd-base: version help"},
		none: []string{"v", "version", "h", "help"}, bare: true, check: fakerootCheck,
	},
	// firejail takes its options as single words, --name or --name=VALUE.
	"firejail": {
		opts: getopt{short: "+c"},
		none: []string{
			"help", "version", "list", "tree", "top", "netstats", "nettrace", "dnstrace", "snitrace", "icmptrace",
			"debug-syscalls", "debug-syscalls32", "debug-syscall-groups", "debug-errnos", "debug-protocols", "debug-caps",
			"seccomp.print", "protocol.print", "profile.print", "cpu.print", "apparmor.print", "caps.print", "fs.print",
			"dns.print", "net.print", "netfilter.print", "netfilter6.print", "get", "put", "ls", "cat", "shutdown",
		},
		bare: true, chdir: []string{"chroot", "private-cwd"}, check: firejailCheck,
	},
	"gdb":     {opts: gdbOpts, reads: gdbArgs, none: []string{"help", "version", "configuration"}, chdir: []string{"cd"}, check: gdbCheck},
	"gosu":    {reads: noOpts(1)},
	"i386":    {opts: archOpts, bare: true, none: []string{"h", "help", "V", "version"}},
	"linux32": {opts: archOpts, bare: true, none: []string{"h", "help", "V", "version"}},
	"linux64": {opts: archOpts, bare: true, none: []string{"h", "help", "V", "version"}},
	"ltrace": {
		opts: getopt{short: "+Cw:cfhiLrStTVba:A:D:e:F:l:n:o:p:s:u:x:", long: "align: config: debug: demangle indent: help library: output: version no-signals where:"},
		none: []string{"h", "help", "V", "version"},
	},
	"nsenter": {
		opts: getopt{short: "+ahVt:m::u::i::n::N:p::C::U::T::S:G:r::w::W::ecFZ", long: "all help version target: mount:: uts:: ipc:: net:: net-socket: pid:: user:: cgroup:: time:: setuid: setgid: root:: wd:: wdns:: env no-fork join-cgroup preserve-credentials keep-caps user-parent follow-context"},
		none: []string{"h", "help", "V", "version"}, bare: true, chdir: []string{"r", "root", "w", "wd", "W", "wdns"},
		check: nsenterCheck,
	},
	"numactl": {
		opts: getopt{short: "+ai:w:p:P:c:N:C:m:slbHS:f:o:L:tM:dDI:uTV", long: "all interleave: weighted-interleave: preferred: preferred-many: cpubind: cpunodebind: physcpubind: membind: show localalloc balancing hardware shm: file: offset: length: strict shmmode: dump dump-nodes shmid: huge touch cpu-compress verify version"},
		none: []string{"s", "show", "H", "hardware", "version", "S", "shm", "f", "file"},
	},
	"pgrphack": {reads: noOpts(0)},
	// pkexec compares its words with its options as they are: what is
	// none of them, "--" and -uUSER among them, is the program, which
	// runs in the home of the user unless --keep-cwd.
	"pkexec": {
		opts: getopt{short: "+u:", long: "help version user: disable-internal-agent keep-cwd"},
		none: []string{"help", "version"}, bare: true, stays: []string{"keep-cwd"},
	},
	"prlimit": {
		opts: getopt{short: "+c::d::e::f::i::l::m::n::q::r::s::t::u::v::x::y::p:o:vVh", long: "pid: output: as:: core:: cpu:: data:: fsize:: locks:: memlock:: msgqueue:: nice:: nofile:: nproc:: rss:: rtprio:: rttime:: sigpending:: stack:: version help noheadings raw verbose"},
		none: []string{"p", "pid", "V", "version", "h", "help"},
	},
	"proxychains":  {opts: getopt{short: "+qf:"}, reads: proxychainsArgs},
	"proxychains4": {opts: getopt{short: "+qf:"}, reads: proxychainsArgs},
	// run0 joins the words of --via-shell and -i for -c of the shell of
	// the user, as ssh does, and expands no $NAME in them.
	"run0": {
		opts:  getopt{short: "+hVu:g:D:i", long: "help version no-ask-password machine: unit: property: description: slice: slice-inherit user: group: nice: chdir: via-shell login setenv: background: pty pty-late pipe shell-prompt-prefix: lightweight: area: empower same-root-dir"},
		none:  []string{"h", "help", "V", "version"},
		shell: []string{"via-shell", "i", "login"}, joins: true, bare: true,
		chdir: []string{"D", "chdir", "i", "login", "u", "user", "area"}, check: unitCheck,
	},
	"setarch": {opts: setarchOpts, reads: setarchArgs, bare: true, none: []string{"h", "help", "V", "version", "list", "show"}},
	"setlock": {opts: getopt{short: "+nNxX"}, operands: 1},
	"setpriv": {
		opts: getopt{short: "+dhV", long: "dump nnp no-new-privs inh-caps: ambient-caps: list-caps ruid: euid: rgid: egid: reuid: regid: clear-groups keep-groups init-groups groups: bounding-set: securebits: pdeathsig: ptracer: selinux-label: apparmor-profile: landlock-access: landlock-rule: seccomp-filter: help reset-env version"},
		none: []string{"d", "dump", "list-caps", "h", "help", "V", "version"},
	},
	"setuidgid": {reads: noOpts(1)},
	"softlimit": {opts: getopt{short: "+a:c:d:f:l:m:o:p:r:s:t:"}},
	"strace": {
		opts: getopt{short: "+a:Ab:cCdDe:E:fFhiI:knNo:O:p:P:qrs:S:tTu:U:vVwxX:yYzZ", long: "columns: output-append-mode detach-on: summary-only summary debug daemonize:: daemonised:: daemonized:: env: follow-forks output-separately help instruction-pointer interruptible: kill-on-exit stack-trace:: stack-traces:: stack-trace-frame-limit: syscall-limit: syscall-number arg-names output: summary-syscall-overhead: attach: trace-path: relative-timestamps:: string-limit: summary-sort-by: absolute-timestamps:: timestamps:: syscall-times:: user: summary-columns: no-abbrev version summary-wall-clock strings-in-hex:: const-print-style: pidns-translation successful-only failed-only failing-only seccomp-bpf tips:: argv0: always-show-pid color: trace: trace-fds: abbrev: verbose: raw: signals: status: read: write: fault: inject: kvm: namespace:: quiet:: silent:: silence:: decode-fds:: decode-pids: secontext::"},
		none: []string{"h", "help", "V", "version"}, check: straceCheck,
	},
	"su-exec": {reads: noOpts(1)},
	"systemd-inhibit": {
		opts: getopt{short: "+h", long: "help version no-ask-password no-pager no-legend json: what: who: why: mode: list"},
		none: []string{"h", "help", "version", "list"},
	},
	// systemd-run runs a service in the directory of its manager unless
	// --same-dir, --scope or --shell keep this one, and expands $NAME in
	// the words of the command; with --shell and no command it runs one.
	"systemd-run": {
		opts:  getopt{short: "+hH:M:C:u:p:rdRE:tTPqvGS", long: "help version no-ask-password user system host: machine: capsule: scope unit: property: description: slice: slice-inherit expand-environment: no-block remain-after-exit wait send-sighup service-type: uid: gid: nice: working-directory: same-dir root-directory: same-root-dir setenv: tty pty pty-late pipe quiet verbose output: json: collect shell job-mode: ignore-failure background: no-pager path-property: socket-property: on-active: on-boot: on-startup: on-unit-active: on-unit-inactive: on-calendar: on-timezone-change on-clock-change timer-property:"},
		none:  []string{"h", "help", "version"},
		shell: []string{"S", "shell"}, stays: []string{"d", "same-dir", "scope", "S", "shell"},
		chdir: []string{"working-directory", "root-directory"}, check: unitCheck, fills: unitFills,
	},
	"taskset": {opts: getopt{short: "+apchV", long: "all-tasks pid cpu-list help version"}, operands: 1, none: []string{"p", "pid", "h", "help", "V", "version"}},
	// torsocks --shell runs a shell, whatever follows it.
	"torsocks": {
		opts: getopt{short: "+u:p:a:P:i6dqh", long: "user: pass: address: port: isolate ipv6 debug quiet shell help version"},
		none: []string{"h", "help", "version"}, shell: []string{"shell"},
	},
	"uclampset": {opts: getopt{short: "+asRp:hm:M:vV", long: "all-tasks pid: system reset-on-fork help verbose version"}, none: []string{"p", "pid", "s", "system", "h", "help", "V", "version"}},
	"unbuffer":  {opts: spawnOpts, reads: unbufferArgs, none: []string{"open", "leaveopen", "pty"}},
	"unshare": {
		opts: getopt{short: "+fhVmuinpCTUrR:w:S:G:cl:", long: "help version mount:: uts:: ipc:: net:: pid:: user:: cgroup:: time:: fork kill-child:: forward-signals mount-proc:: mount-binfmt:: map-user: map-users: map-group: map-groups: map-root-user map-current-user map-auto map-subids owner: propagation: setgroups: keep-caps setuid: setgid: root: wd: monotonic: boottime: load-interp:"},
		none: []string{"h", "help", "V", "version"}, bare: true, chdir: []string{"R", "root", "w", "wd"},
	},
	"valgrind": {reads: valgrindArgs, none: []string{"h", "help", "help-debug", "help-dyn-options", "version"}},
	"x86_64":   {opts: archOpts, bare: true, none: []string{"h", "help", "V", "version"}},
}

// The options of the wrappers above that more than their entry reads.
var (
	chpstOpts   = getopt{short: "+u:U:b:e:m:d:o:p:f:c:r:t:/:C:n:l:L:vP012V"}
	chrtOpts    = getopt{short: "+abdD:efiphmoP:T:rRvV", long: "all-tasks batch deadline ext fifo idle pid help max other rr sched-runtime: sched-period: sched-deadline: reset-on-fork verbose version"}
	setarchOpts = getopt{short: "+hVv3BFILRSTXZp:", long: "help version verbose addr-no-randomize fdpic-funcptrs mmap-page-zero addr-compat-layout read-implies-exec 32bit short-inode whole-seconds sticky-timeouts 3gb 4gb uname-2.6 list show:: pid:"}
	// archOpts are those of setarch run as the name of an architecture,
	// which takes no architecture and no --list, --show or --pid.
	archOpts = getopt{short: "+hVv3BFILRSTXZ", long: "help version verbose addr-no-randomize fdpic-funcptrs mmap-page-zero addr-compat-layout read-implies-exec 32bit short-inode whole-seconds sticky-timeouts 3gb 4gb uname-2.6"}
	// gdbOpts are read as getopt_long_only reads them: -name or --name.
	gdbOpts = getopt{long: "tui readnow readnever r quiet q silent nh nx n batch-silent batch fullname f annotate: help se: symbols: s: exec: e: core: c: pid: p: command: eval-command: version configuration x: ex: init-command: init-eval-command: ix: iex: early-init-command: early-init-eval-command: eix: eiex: tclcommand: enable-external-editor editor-command: ui: interpreter: i: directory: d: data-directory: D: cd: tty: baud: b: nw nowindows w windows statistics write args no-escape-args l: return-child-result binary-output"}
	// spawnOpts are those of Expect's spawn, -name or a unique prefix.
	spawnOpts = getopt{long: "console ignore: leaveopen: noecho nottycopy nottyinit open: pty"}
)

// rooted lists the options of the wrappers above that run the command
// under another root, as chroot does (see roots).
var rooted = map[string][]string{
	"chpst":       {"/"},
	"firejail":    {"chroot"},
	"nsenter":     {"r", "root"},
	"systemd-run": {"root-directory"},
	"unshare":     {"R", "root"},
}

// hands tells whether w hands the command more than its words: an
// environment, a shell, code in the value of an option, or a shell of its
// own when there is no command.
func (w wrapper) hands() bool {
	return w.env || w.shell != nil || w.bare || w.check != nil
}

// readFrom reads args from word i on as g does: the options, with their
// indexes in args, and the index of the command, len(args) for none.
func readFrom(g getopt, args []string, i int) ([]option, int) {
	opts, ops := g.read(args[i:])
	for k := range opts {
		opts[k].word += i
	}
	if len(ops) == 0 {
		return opts, len(args)
	}
	return opts, i + ops[0]
}

// noOpts reads no options: the first n words are the operands before the
// command whatever they hold, as setuidgid ACCOUNT CMD reads them.
func noOpts(n int) func(args []string) ([]option, int) {
	return func(args []string) ([]option, int) {
		return nil, min(n, len(args))
	}
}

// chrtArgs reads chrt [OPTIONS] [PRIORITY] COMMAND: the first operand is
// the priority only when it is a number and a word follows it.
func chrtArgs(args []string) ([]option, int) {
	opts, cmd := readFrom(chrtOpts, args, 0)
	if cmd+1 < len(args) && digits(args[cmd]) {
		cmd++
	}
	return opts, cmd
}

// setarchArgs reads setarch [ARCH] [OPTIONS] [PROGRAM]: a first word that
// is no option is the architecture, before the options.
func setarchArgs(args []string) ([]option, int) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return readFrom(setarchOpts, args, 1)
	}
	return readFrom(setarchOpts, args, 0)
}

// proxychainsArgs reads proxychains4 [-q] [-f FILE] CMD: up to two words
// starting with "-", by the letter after it whatever follows, -f taking
// the next word for its file. Any other such word is the program.
func proxychainsArgs(args []string) ([]option, int) {
	var opts []option
	i := 0
	for range 2 {
		if i >= len(args) || !strings.HasPrefix(args[i], "-") || len(args[i]) < 2 {
			break
		}
		switch args[i][1] {
		case 'q':
			opts = append(opts, option{name: "q", word: i})
			i++
		case 'f':
			if i+1 >= len(args) {
				return opts, len(args)
			}
			opts = append(opts, option{name: "f", value: args[i+1], word: i + 1})
			i += 2
		default:
			return opts, i
		}
	}
	return opts, min(i, len(args))
}

// unbufferArgs reads unbuffer [-p] PROGRAM: the words after -p go to
// Expect's spawn, which reads options of its own before the program.
func unbufferArgs(args []string) ([]option, int) {
	var opts []option
	i := 0
	if len(args) > 0 && args[0] == "-p" {
		opts = append(opts, option{name: "p"})
		i = 1
	}
	for ; i < len(args) && strings.HasPrefix(args[i], "-"); i++ {
		o := option{word: i}
		var takes int
		if o.name, takes = spawnOpts.longOpt(args[i][1:]); takes == 1 && i+1 < len(args) {
			i++
			o.value, o.word = args[i], i
		}
		opts = append(opts, o)
	}
	return opts, i
}

// valgrindArgs reads valgrind as its launcher finds the program: the first
// word not starting with "-", or the one after "--". Its options are
// --name=VALUE.
func valgrindArgs(args []string) ([]option, int) {
	var opts []option
	for i, a := range args {
		switch {
		case a == "--":
			return opts, i + 1
		case !strings.HasPrefix(a, "-"):
			return opts, i
		}
		name, v, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		opts = append(opts, option{name: name, value: v, word: i})
	}
	return opts, len(args)
}

// gdbArgs reads gdb as getopt_long_only reads its options, among the
// operands too: the first operand is the program; after --args or
// --no-escape-args the next word is, and the rest are its arguments.
func gdbArgs(args []string) ([]option, int) {
	var opts []option
	first := -1
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			if first < 0 {
				first = i + 1
			}
			return opts, min(first, len(args))
		case len(a) < 2 || a[0] != '-':
			if first < 0 {
				first = i
			}
			continue
		}
		name, v, eq := strings.Cut(strings.TrimPrefix(a[1:], "-"), "=")
		o := option{value: v, word: i}
		var takes int
		if o.name, takes = gdbOpts.longOpt(name); takes == 1 && !eq && i+1 < len(args) {
			i++
			o.value, o.word = args[i], i
		}
		opts = append(opts, o)
		if o.name == "args" || o.name == "no-escape-args" {
			return opts, i + 1
		}
	}
	if first < 0 {
		return opts, len(args)
	}
	return opts, first
}

// straceCheck returns the command strace -o '|CMD' (or '!CMD') pipes its
// output to through sh -c and marks the variables of -E NAME=VALUE.
func straceCheck(p *parser, opts []option, _ []string, static []bool, _ int) []string {
	var code []string
	for _, o := range opts {
		switch o.name {
		case "o", "output":
			switch {
			case strings.HasPrefix(o.value, "|"), strings.HasPrefix(o.value, "!"):
				if static[o.word] {
					code = append(code, o.value[1:])
				} else {
					p.mark(dynComputed)
				}
			case !static[o.word] && !plainStart(o.value):
				// It may start with | once expanded.
				p.mark(dynComputed)
			}
		case "E", "env":
			p.setenv(o.value, static[o.word])
		}
	}
	return code
}

// plainStart tells whether the source form of a word starts with a
// character the shell keeps as it is, so that the word starts with it once
// expanded too: not an expansion, a glob or braces, nor | or ! itself.
func plainStart(s string) bool {
	return s != "" && !strings.ContainsRune("$`*?[{|!\\", rune(s[0]))
}

// setenv marks NAME=VALUE that a wrapper puts in the environment of the
// command as an assignment to NAME, of VALUE when the word is static; a
// name the shell builds may be any.
func (p *parser) setenv(v string, static bool) {
	name, value, ok := strings.Cut(v, "=")
	switch {
	case !ok:
	case strings.ContainsAny(name, "$`\\*?[{\"'"):
		p.mark(dynComputed)
	case static:
		p.assignedTo(name, value)
	default:
		p.assignedText(name, value)
	}
}

// unitCheck looks at the properties of the units of systemd-run and run0:
// Exec…= holds a command line of systemd's syntax and EnvironmentFile=
// any variables, which the line does not show; Environment= and
// --setenv set the variables they name.
func unitCheck(p *parser, opts []option, _ []string, static []bool, _ int) []string {
	for _, o := range opts {
		switch o.name {
		case "p", "property", "path-property", "socket-property", "timer-property":
			name, value, _ := strings.Cut(o.value, "=")
			switch {
			case !static[o.word] && strings.ContainsAny(name, "$`\\*?[{\"'"),
				strings.HasPrefix(name, "Exec"), name == "EnvironmentFile",
				name == "Environment" && strings.ContainsAny(value, "\"'\\$%"):
				p.mark(dynComputed)
			case name == "Environment":
				for _, f := range strings.Fields(value) {
					p.setenv(f, static[o.word])
				}
			}
		case "E", "setenv":
			p.setenv(o.value, static[o.word])
		}
	}
	return nil
}

// unitFills marks the words of the command of systemd-run with a $ in
// them: it expands $NAME and ${NAME} there from the environment of the
// unit, unless --expand-environment says no.
func unitFills(_ *parser, opts []option, argv []string, static, split []bool) {
	for _, o := range slices.Backward(opts) {
		if o.name == "expand-environment" {
			switch o.value {
			case "0", "no", "n", "false", "f", "off":
				return
			}
			break
		}
	}
	for i, w := range argv {
		if strings.Contains(w, "$") {
			static[i], split[i] = false, true
		}
	}
}

// fakerootCheck returns the code fakeroot runs with eval: echo of the
// value of -l, which it takes for the library, and the line that starts
// faked: the value of -f for the program, -s and -i for its files. The
// library goes to LD_PRELOAD of the command, as rebind.
func fakerootCheck(p *parser, opts []option, _ []string, static []bool, _ int) []string {
	var code []string
	faked, line, in := "faked", "", ""
	evals := false
	for _, o := range opts {
		switch o.name {
		case "l", "lib", "f", "faked", "s", "i":
			if !static[o.word] {
				p.mark(dynComputed)
				continue
			}
		}
		switch o.name {
		case "l", "lib":
			code = append(code, "echo "+o.value)
			p.mark(dynRebind)
		case "f", "faked":
			faked, evals = o.value, true
		case "s":
			line += " --save-file " + o.value
			evals = true
		case "i":
			line += " --load"
			in = " <" + o.value
			evals = true
		case "u", "unknown-is-real":
			line += " --unknown-is-real"
		}
	}
	if evals {
		code = append(code, faked+line+in)
	}
	return code
}

// firejailCheck marks the variables of --env=NAME=VALUE.
func firejailCheck(p *parser, opts []option, _ []string, static []bool, _ int) []string {
	for _, o := range opts {
		if o.name == "env" {
			p.setenv(o.value, static[o.word])
		}
	}
	return nil
}

// gdbCheck returns the program of -e and -se, which gdb runs as it runs
// its operand, and the command line of --no-escape-args, which it hands
// to a shell as it is.
func gdbCheck(p *parser, opts []option, args []string, static []bool, cmd int) []string {
	var code []string
	for _, o := range opts {
		switch {
		case o.name != "e" && o.name != "exec" && o.name != "se":
		case static[o.word]:
			code = append(code, escaped([]string{o.value}))
		default:
			p.mark(dynComputed)
		}
	}
	if has(opts, "no-escape-args") && cmd < len(args) {
		if slices.Contains(static[cmd:], false) {
			p.mark(dynComputed)
		} else {
			code = append(code, strings.Join(args[cmd:], " "))
		}
	}
	return code
}

// chpstCheck marks chpst -e DIR: the variables of the files there may be
// any.
func chpstCheck(p *parser, opts []option, _ []string, _ []bool, _ int) []string {
	if has(opts, "e") {
		p.mark(dynComputed)
	}
	return nil
}

// envdirCheck marks envdir DIR CMD as chpstCheck does chpst -e.
func envdirCheck(p *parser, _ []option, _ []string, _ []bool, _ int) []string {
	p.mark(dynComputed)
	return nil
}

// xargsFills marks the words of the command of xargs that it fills in:
// with -I, -i or --replace (or -J of BSD) a word that holds the replace
// string becomes any text, an option of the wrapper behind xargs among
// them, taken for split. Without them, unless -L, -l or -n came after,
// xargs appends what it reads to the command, which may make a program or
// code of it: see appended.
func xargsFills(p *parser, opts []option, argv []string, static, split []bool) {
	replace := false
	var pats []string
	for _, o := range opts {
		switch o.name {
		case "I", "J", "i", "replace":
			pat := o.value
			if pat == "" && (o.name == "i" || o.name == "replace") {
				pat = "{}"
			}
			if strings.ContainsAny(pat, "$`") {
				// Made at run time, it may be in any word.
				p.mark(dynComputed)
			}
			pats = append(pats, pat)
			replace = replace || o.name != "J"
		case "L", "l", "max-lines":
			replace = false
		case "n", "max-args":
			// -n 1 after -I is left out; any other -n ends it.
			replace = replace && o.value == "1"
		}
	}
	for i, w := range argv {
		if slices.ContainsFunc(pats, func(pat string) bool { return strings.Contains(w, pat) }) {
			static[i], split[i] = false, true
		}
	}
	if !replace {
		p.appended(argv, static, split)
	}
}

// appended marks the command argv of xargs when the words xargs appends
// to it may make a program or code of it: xargs sh -c, xargs env, xargs
// sudo. It walks argv as call does, with one more word made at run time,
// on a parser of its own: only the mark it gets is kept.
func (p *parser) appended(argv []string, static, split []bool) {
	q := &parser{kinds: map[string]bool{}, cwd: p.cwd, home: p.home, remote: p.remote}
	argv = append(slices.Clip(argv), "$(xargs)")
	static = append(slices.Clip(static), false)
	split = append(slices.Clip(split), true)
	for argv != nil {
		_, _, local := q.handed(argv, static, nil)
		q.shellC(argv[:local], static[:local])
		q.program(argv, static, nil)
		argv, static, split = q.next(argv, static, split)
	}
	if q.kinds[dynComputed] {
		p.mark(dynComputed)
	}
}

// logins start a shell as another user or group, or record one: with no
// command it reads its commands from stdin, as bash does.
var logins = map[string]func(args []string) bool{
	"newgrp":  newgrpReads,
	"runuser": runuserReads,
	"script":  scriptReads,
	"sg":      sgReads,
	"su":      suReads,
}

// suReads tells whether su starts a shell that reads stdin: no string of
// -c, and no script or file among the words after the user, which go to
// that shell.
func suReads(args []string) bool {
	opts, ops := suOpts.read(args)
	if has(opts, "c", "command", "session-command", "h", "help", "V", "version") {
		return false
	}
	if len(ops) > 0 && args[ops[0]] == "-" {
		ops = ops[1:]
	}
	if len(ops) < 2 {
		return true
	}
	shell := make([]string, len(ops)-1)
	for k, w := range ops[1:] {
		shell[k] = args[w]
	}
	_, _, stdin := shellArgs(shell)
	return stdin
}

// runuserReads is suReads of runuser without -u, which runs a command or
// fails without one.
func runuserReads(args []string) bool {
	if opts, _ := suOpts.read(args); has(opts, "u", "user") {
		return false
	}
	return suReads(args)
}

// sgReads tells whether sg [-] GROUP has no command, and so starts a
// shell.
func sgReads(args []string) bool {
	if len(args) > 0 && (args[0] == "-" || args[0] == "-l") {
		args = args[1:]
	}
	return len(args) == 1 && !strings.HasPrefix(args[0], "-")
}

// sgCommand finds the string of sg [-] GROUP [-c] CMD, which sg runs with
// sh -c; the words after it are left out.
func sgCommand(args []string) []piece {
	i := 0
	if len(args) > 0 && (args[0] == "-" || args[0] == "-l") {
		i = 1
	}
	if i >= len(args) || strings.HasPrefix(args[i], "-") {
		return nil
	}
	i++ // the group
	switch {
	case len(args)-i > 1 && args[i] == "-c":
		i++
	case i == len(args):
		return nil
	}
	return []piece{{args[i], []int{i}}}
}

// newgrpReads tells whether newgrp [-] [GROUP] starts a shell: it runs no
// command, and fails on an option.
func newgrpReads(args []string) bool {
	if len(args) > 0 && (args[0] == "-" || args[0] == "-l") {
		args = args[1:]
	}
	return len(args) == 0 || !strings.HasPrefix(args[0], "-")
}

// scriptReads tells whether script runs a shell, whose commands it reads
// from its stdin: with no -c.
func scriptReads(args []string) bool {
	opts, _ := scriptOpts.read(args)
	return !has(opts, "c", "command", "h", "help", "V", "version")
}
