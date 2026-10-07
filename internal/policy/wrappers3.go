package policy

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

func init() {
	maps.Copy(wrappers, boxWrappers)
	rooted["capsh"] = []string{"chroot"}
}

// boxWrappers run a command in a box of its own: another namespace, a
// sandbox, a session, a container, a pod or a machine. They read their
// words as their sources do: libcap 2.78 (capsh), iproute2 7.1 (ip),
// bubblewrap 0.11, systemd 261 (systemd-nspawn, machinectl), dbus 1.16,
// OpenSSH 10 (ssh-agent), libeatmydata, flatpak-xdg-utils (flatpak-spawn),
// toolbox, distrobox 1.8, Docker CLI 29, Docker Compose 5, nerdctl 2, Podman 6,
// kubectl 1.36, oc of OpenShift 4, LXD 6 (lxc) and Incus 7. The command of a
// container is a command of this machine to the policies, not one of
// Remote: its words name files of this one, which its volumes share, and
// its redirections write here.
var boxWrappers = map[string]wrapper{
	// bwrap runs its command under a root of its own (see reroots).
	"bwrap": {reads: bwrapArgs, chdir: []string{"chdir"}, check: bwrapCheck},
	"capsh": {reads: capshArgs, prog: capshShell, chdir: []string{"chroot"}},
	"dbus-run-session": {
		opts: getopt{short: "+h?", long: "help version config-file: dbus-daemon:"},
		none: []string{"h", "?", "help", "version"}, check: dbusCheck,
	},
	// distrobox enter with no command runs a login shell in the container,
	// and so does distrobox ephemeral (see ephemeralArgs); create and
	// assemble run code of their options and of a file (see createCode).
	"distrobox": {
		reads: func(args []string) ([]option, int) {
			switch {
			case len(args) > 0 && args[0] == "ephemeral":
				return ephemeralArgs(args, 1)
			case len(args) > 0 && args[0] == "create":
				return createArgs(args, 1)
			case len(args) > 0 && args[0] == "assemble":
				return assembleArgs(args, 1)
			case len(args) == 0 || args[0] != "enter":
				return nil, len(args)
			}
			opts, cmd := distroboxEnter(args, 1)
			return append(opts, option{name: "enter"}), cmd
		},
		none: distroboxNone, bare: true, attach: []string{"enter", "ephemeral"},
		chdir: []string{"nw", "no-workdir"}, check: distroboxSubCheck,
	},
	"distrobox-assemble": {
		reads: func(args []string) ([]option, int) { return assembleArgs(args, 0) },
		none:  []string{"h", "help", "V", "version", "?"}, check: assembleCheck,
	},
	"distrobox-create": {
		reads: func(args []string) ([]option, int) { return createArgs(args, 0) },
		none:  distroboxNone, check: createCheck,
	},
	"distrobox-enter": {
		reads: func(args []string) ([]option, int) { return distroboxEnter(args, 0) },
		none:  distroboxNone, bare: true, chdir: []string{"nw", "no-workdir"}, check: distroboxCheck,
	},
	"distrobox-ephemeral": {
		reads: func(args []string) ([]option, int) { return ephemeralArgs(args, 0) },
		none:  distroboxNone, bare: true, attach: []string{"ephemeral"}, check: ephemeralCheck(0),
	},
	"docker":         dockerCLI.wrapper(),
	"docker-compose": composeWrapper,
	"eatmydata":      {reads: eatmydataArgs},
	"flatpak-spawn":  {reads: flatpakSpawnArgs, none: []string{"h", "?", "help", "help-all"}, chdir: []string{"directory"}, check: flatpakSpawnCheck},
	"incus":          lxcWrapper,
	"ip":             {reads: ipArgs, check: ipCheck},
	"kubectl":        kubeWrapper(false),
	"lxc":            lxcWrapper,
	// machinectl shell with no program runs the login shell of the user.
	"machinectl":     {opts: machinectlOpts, reads: machinectlArgs, none: []string{"h", "help", "version"}, bare: true, attach: []string{"shell"}, check: envCheck("E", "setenv")},
	"nerdctl":        nerdctlCLI.wrapper(),
	"oc":             kubeWrapper(true),
	"podman":         podmanCLI.wrapper(),
	"podman-compose": composeWrapper,
	"ssh-agent":      {opts: getopt{short: "+cDdksTuUVE:a:O:P:t:"}, none: []string{"c", "D", "d", "k", "s", "u", "V"}},
	// systemd-nspawn runs its command under the root of the container (see
	// reroots), in its / unless --chdir, and a shell with none; with -b
	// its words are those of the init it boots.
	"systemd-nspawn": {
		opts: nspawnOpts, none: []string{"h", "help", "version", "b", "boot"}, bare: true,
		chdir: []string{"chdir"}, stays: []string{}, check: envCheck("E", "setenv"),
	},
	// toolbox enter runs a shell in the container.
	"toolbox": {opts: toolboxOpts, reads: toolboxArgs, none: []string{"h", "help", "version"}, bare: true, attach: []string{"enter"}},
}

// reroots tell whether a wrapper runs its command under another root, by
// options rooted cannot name: bwrap builds one of its options, the
// container of systemd-nspawn is one, podman --rootfs and nerdctl --rootfs
// take a directory for it, and so do the properties RootDirectory= and
// RootImage= of a unit.
var reroots = map[string]func(opts []option) bool{
	"bwrap":          func(opts []option) bool { return !has(opts, "help", "version") },
	"nerdctl":        func(opts []option) bool { return has(opts, "rootfs") },
	"podman":         func(opts []option) bool { return has(opts, "rootfs") },
	"run0":           unitRooted,
	"systemd-nspawn": func(opts []option) bool { return !has(opts, "h", "help", "version") },
	"systemd-run":    unitRooted,
}

// programOf is the program w runs in place of the word of its command (see
// prog), nil for that word.
func (w wrapper) programOf(opts []option, static []bool) ([]string, bool) {
	if w.prog == nil {
		return nil, false
	}
	return w.prog(opts, static)
}

// unitRooted tells whether -p or --property of systemd-run or run0 sets
// RootDirectory= or RootImage= of the unit.
func unitRooted(opts []option) bool {
	return slices.ContainsFunc(opts, func(o option) bool {
		name, _, _ := strings.Cut(o.value, "=")
		return (o.name == "p" || o.name == "property") && (name == "RootDirectory" || name == "RootImage")
	})
}

// nsenterCheck marks nsenter -e, which takes the environment of the target
// process, PATH and LD_PRELOAD among it.
func nsenterCheck(p *parser, opts []option, _ []string, _ []bool, _ int) []string {
	if has(opts, "e", "env") {
		p.mark(dynRebind)
	}
	return nil
}

// envCheck marks the variables NAME=VALUE of the options names put in the
// environment of the command, as env does.
func envCheck(names ...string) func(p *parser, opts []option, args []string, static []bool, cmd int) []string {
	return func(p *parser, opts []option, _ []string, static []bool, _ int) []string {
		for _, o := range opts {
			if slices.Contains(names, o.name) {
				p.setenv(o.value, static[o.word])
			}
		}
		return nil
	}
}

// capshArgs reads capsh as it reads its words, one by one, each an option
// of its own: --NAME or --NAME=VALUE. "--" and "-+" run its shell with the
// words after them, in place of that word (see capshShell); "==" and "=+"
// run capsh again, which reads them anew. --help, -h and --license print
// and exit; so does a word capsh does not know, read here as an option, as
// a newer capsh may know it.
func capshArgs(args []string) ([]option, int) {
	var opts []option
	for i, a := range args {
		switch a {
		case "--", "-+":
			return opts, i
		case "--help", "-h", "--license":
			return opts, len(args)
		case "==", "=+":
			opts = append(opts, option{name: a, word: i})
			continue
		}
		name, v, _ := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		opts = append(opts, option{name: name, value: v, word: i})
	}
	return opts, len(args)
}

// capshShell is the shell of capsh --: /bin/bash, or the value of the last
// --shell= since capsh last ran itself again.
func capshShell(opts []option, static []bool) ([]string, bool) {
	shell, st := "/bin/bash", true
	for _, o := range opts {
		switch o.name {
		case "==", "=+":
			shell, st = "/bin/bash", true
		case "shell":
			shell, st = o.value, static[o.word]
		}
	}
	return []string{shell}, st
}

// ipOptions are the options of ip in the order it matches a word to them:
// by a prefix of the name, "-" included, or by the whole name (exact).
// value takes the next word; stops prints and exits, or runs no OBJECT.
var ipOptions = []struct {
	name                string
	exact, value, stops bool
}{
	{name: "-loops", value: true}, {name: "-family", value: true},
	{name: "-4", exact: true}, {name: "-6", exact: true}, {name: "-0", exact: true},
	{name: "-M", exact: true}, {name: "-B", exact: true},
	{name: "-human"}, {name: "-human-readable"}, {name: "-iec"}, {name: "-stats"}, {name: "-statistics"},
	{name: "-details"}, {name: "-resolve"}, {name: "-oneline"}, {name: "-timestamp"}, {name: "-tshort"},
	{name: "-Version", stops: true}, {name: "-force"}, {name: "-batch", value: true, stops: true},
	{name: "-brief"}, {name: "-json"}, {name: "-pretty"}, {name: "-rcvbuf", value: true},
	{name: "-color"}, {name: "-help", stops: true}, {name: "-netns", value: true},
	{name: "-Numeric"}, {name: "-all"}, {name: "-echo", exact: true},
}

// The objects of ip, the commands of ip netns and those of ip vrf, in the
// order ip matches a word to them.
var (
	ipObjects = []string{"address", "addrlabel", "maddress", "route", "rule", "neighbor", "neighbour", "ntable", "ntbl", "link", "l2tp", "fou", "ila", "macsec", "tunnel", "tunl", "tuntap", "tap", "token", "tcpmetrics", "tcp_metrics", "monitor", "xfrm", "mroute", "mrule", "netns", "netconf", "vrf", "sr", "nexthop", "mptcp", "ioam", "help", "stats"}
	ipNetns   = []string{"list", "show", "lst", "list-id", "help", "add", "set", "delete", "identify", "pids", "exec", "monitor", "attach"}
	ipVrf     = []string{"identify", "pids", "exec", "show", "lst", "list", "help"}
)

// ipMatch is the first of names that word is a prefix of, as matches of
// iproute2 finds it; "" for none.
func ipMatch(word string, names []string) string {
	for _, n := range names {
		if word != "" && strings.HasPrefix(n, word) {
			return n
		}
	}
	return ""
}

// ipOption finds the option of ip a word starting with "-" is, its
// leading "--" taken for "-": -color takes =always, =auto, =never or no
// value. false for one ip fails on.
func ipOption(a string) (name string, value, stops, ok bool) {
	if len(a) > 1 && a[1] == '-' {
		a = a[1:]
	}
	for _, o := range ipOptions {
		match := a == o.name || !o.exact && strings.HasPrefix(o.name, a)
		if o.name == "-color" {
			head, v, _ := strings.Cut(a, "=")
			match = strings.HasPrefix(o.name, head) && (v == "" || v == "always" || v == "auto" || v == "never")
		}
		if match {
			return o.name[1:], o.value, o.stops, true
		}
	}
	return "", false, false, false
}

// ipArgs reads ip [OPTIONS] OBJECT COMMAND as iproute2 does: ip netns exec
// NAME CMD and ip vrf exec NAME CMD run CMD, ip -all netns exec CMD runs it
// in every namespace. -batch runs the commands of its file instead.
func ipArgs(args []string) ([]option, int) {
	var opts []option
	all := false
	i := 0
options:
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i++
			break options
		case !strings.HasPrefix(a, "-"):
			break options
		}
		name, value, stops, ok := ipOption(a)
		if !ok || value && i+1 == len(args) {
			return opts, len(args)
		}
		o := option{name: name, word: i}
		if value {
			i++
			o.value, o.word = args[i], i
		}
		opts = append(opts, o)
		if stops {
			return opts, len(args)
		}
		all = all || name == "all"
	}
	if i+1 >= len(args) {
		return opts, len(args)
	}
	var cmds []string
	switch ipMatch(args[i], ipObjects) {
	case "netns":
		cmds = ipNetns
	case "vrf":
		cmds, all = ipVrf, false
	}
	if cmds == nil || ipMatch(args[i+1], cmds) != "exec" {
		return opts, len(args)
	}
	if all {
		return opts, i + 2
	}
	// The word after exec names the namespace or the VRF.
	return opts, min(i+3, len(args))
}

// bwrapTakes is how many words each option of bwrap takes after it; bwrap
// matches them whole.
var bwrapTakes = map[string]int{
	"help": 0, "version": 0, "level-prefix": 0, "unshare-all": 0, "unshare-user": 0, "unshare-user-try": 0,
	"unshare-ipc": 0, "unshare-pid": 0, "unshare-net": 0, "unshare-uts": 0, "unshare-cgroup": 0,
	"unshare-cgroup-try": 0, "share-net": 0, "disable-userns": 0, "assert-userns-disabled": 0, "clearenv": 0,
	"new-session": 0, "die-with-parent": 0, "as-pid-1": 0,

	"args": 1, "argv0": 1, "chdir": 1, "remount-ro": 1, "overlay-src": 1, "tmp-overlay": 1, "ro-overlay": 1,
	"proc": 1, "exec-label": 1, "file-label": 1, "dev": 1, "tmpfs": 1, "mqueue": 1, "dir": 1, "lock-file": 1,
	"sync-fd": 1, "block-fd": 1, "userns-block-fd": 1, "info-fd": 1, "json-status-fd": 1, "seccomp": 1,
	"add-seccomp-fd": 1, "userns": 1, "userns2": 1, "pidns": 1, "unsetenv": 1, "uid": 1, "gid": 1,
	"hostname": 1, "cap-add": 1, "cap-drop": 1, "perms": 1, "size": 1,

	"bind": 2, "bind-try": 2, "dev-bind": 2, "dev-bind-try": 2, "ro-bind": 2, "ro-bind-try": 2, "bind-fd": 2,
	"ro-bind-fd": 2, "file": 2, "bind-data": 2, "ro-bind-data": 2, "symlink": 2, "setenv": 2, "chmod": 2,

	"overlay": 3,
}

// bwrapArgs reads bwrap as it reads its words: options matched whole, each
// with the words it takes after it, up to "--" or the first word that is
// no option. The second and third word of an option are options of no
// name. --help and --version print and exit, and so does an option bwrap
// does not know or one short of its words.
func bwrapArgs(args []string) ([]option, int) {
	var opts []option
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return opts, i + 1
		case !strings.HasPrefix(a, "-"):
			return opts, i
		}
		name, _ := strings.CutPrefix(a, "--")
		n, ok := bwrapTakes[name]
		if !ok || name == a || i+n >= len(args) {
			return opts, len(args)
		}
		o := option{name: name, word: i}
		if n > 0 {
			o.value, o.word = args[i+1], i+1
		}
		opts = append(opts, o)
		for k := 2; k <= n; k++ {
			opts = append(opts, option{value: args[i+k], word: i + k})
		}
		if name == "help" || name == "version" {
			return opts, len(args)
		}
		i += n
	}
	return opts, len(args)
}

// bwrapCheck marks the variables of --setenv NAME VALUE, and the options
// --args reads from a descriptor, which may be any.
func bwrapCheck(p *parser, opts []option, args []string, static []bool, _ int) []string {
	for _, o := range opts {
		switch o.name {
		case "setenv":
			p.setenv(o.value+"="+args[o.word+1], static[o.word] && static[o.word+1])
		case "args":
			p.mark(dynComputed)
		}
	}
	return nil
}

// nspawnOpts are the options of systemd-nspawn.
var nspawnOpts = getopt{
	short: "+hqD:xi:abE:u:M:S:Unp:Z:L:jP",
	long:  "help version quiet no-pager settings: cleanup no-ask-password directory: template: ephemeral image: image-policy: mstack: oci-bundle: read-only volatile:: root-hash: root-hash-sig: verity-data: pivot-root: as-pid2 boot chdir: setenv: uid: kill-signal: notify-ready: suppress-sync: machine: hostname: uuid: slice: property: register: keep-unit private-users:: private-users-ownership: private-users-chown:: private-users-delegate: private-network network-interface: network-macvlan: network-ipvlan: network-veth network-veth-extra: network-bridge: network-zone: network-namespace-path: port: capability: drop-capability: ambient-capability: no-new-privileges: system-call-filter: restrict-address-families: selinux-context: selinux-apifs-context: rlimit: oom-score-adjust: cpu-affinity: personality: resolv-conf: timezone: link-journal: forward-journal: forward-journal-max-use: forward-journal-keep-free: forward-journal-max-file-size: forward-journal-max-files: bind: bind-ro: inaccessible: tmpfs: overlay: overlay-ro: bind-user: bind-user-shell: bind-user-group: console: pipe background: set-credential: load-credential: share-system user:: system",
}

// dbusCheck returns the program of --dbus-daemon, which dbus-run-session
// runs for the bus.
func dbusCheck(p *parser, opts []option, _ []string, static []bool, _ int) []string {
	var code []string
	for _, o := range opts {
		switch {
		case o.name != "dbus-daemon":
		case static[o.word]:
			code = append(code, escaped([]string{o.value}))
		default:
			p.mark(dynComputed)
		}
	}
	return code
}

// eatmydataArgs reads eatmydata [--] COMMAND.
func eatmydataArgs(args []string) ([]option, int) {
	if len(args) > 0 && args[0] == "--" {
		return nil, 1
	}
	return nil, 0
}

// flatpakSpawnArgs reads flatpak-spawn as it splits its words: the options
// are the words starting with "-" before the first that does not, which is
// the command; an option has its value in its word, after "=".
func flatpakSpawnArgs(args []string) ([]option, int) {
	var opts []option
	for i, a := range args {
		if !strings.HasPrefix(a, "-") {
			return opts, i
		}
		name, v, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		opts = append(opts, option{name: name, value: v, word: i})
	}
	return opts, len(args)
}

// flatpakSpawnCheck marks the variables of --env=NAME=VALUE, those --env-fd
// reads, which may be any, and another /app or /usr for the programs of
// the command (--app-path, --usr-path).
func flatpakSpawnCheck(p *parser, opts []option, _ []string, static []bool, _ int) []string {
	for _, o := range opts {
		switch o.name {
		case "env":
			p.setenv(o.value, static[o.word])
		case "env-fd":
			p.mark(dynComputed)
		case "app-path", "usr-path":
			p.mark(dynRebind)
		}
	}
	return nil
}

// cobraSub finds the subcommand of a program built with cobra as cobra
// does: the first word that is no flag and no value of one, where --NAME
// and a flag of one letter take the next word for their value unless
// bools names them (cobra takes it so for a flag it does not know too) or
// that word is one of subs, as a flag a newer version adds may take none.
// len(args) for none; "--" ends the flags with none.
func cobraSub(args []string, bools []string, subs ...string) int {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--":
			return len(args)
		case !strings.HasPrefix(a, "-"):
			return i
		case strings.Contains(a, "="):
		case strings.HasPrefix(a, "--"), len(a) == 2:
			if !slices.Contains(bools, strings.TrimLeft(a, "-")) && i+1 < len(args) && !slices.Contains(subs, args[i+1]) {
				i++
			}
		}
	}
	return len(args)
}

// readAll reads args from word from on as g does, options among the
// operands unless g says "+": the options and the operands by their index
// in args, and the "--" that ended the options, -1 for none.
func readAll(g getopt, args []string, from int) (opts []option, ops []int, dash int) {
	opts, rel := g.read(args[from:])
	for k := range opts {
		opts[k].word += from
	}
	for _, r := range rel {
		ops = append(ops, from+r)
	}
	for d := from; d < len(args); d++ {
		if args[d] == "--" && !slices.Contains(ops, d) && !slices.ContainsFunc(opts, func(o option) bool { return o.word == d }) {
			return opts, ops, d
		}
	}
	return opts, ops, -1
}

// holds tells whether g has an option of that name.
func (g getopt) holds(name string) bool {
	if len(name) == 1 && name != ":" && name != "+" && strings.Contains(g.short, name) {
		return true
	}
	return slices.Contains(strings.Fields(strings.ReplaceAll(g.long, ":", "")), name)
}

// strange marks an option g does not hold, but for the markers of a
// reader: a version that has one may take a value with it, which moves the
// command among the words.
func (p *parser) strange(g getopt, opts []option, markers ...string) {
	if slices.ContainsFunc(opts, func(o option) bool {
		return o.name != "" && !g.holds(o.name) && !slices.Contains(markers, o.name)
	}) {
		p.mark(dynComputed)
	}
}

// toolboxOpts are the options of toolbox run and of toolbox, which run
// reads too, toolboxBools those that take no value.
var (
	toolboxOpts  = getopt{short: "+c:d:r:yvh", long: "container: distro: preserve-fds: release: assumeyes log-level: log-podman verbose help version"}
	toolboxBools = []string{"y", "assumeyes", "log-podman", "v", "verbose", "h", "help", "version"}
)

// toolboxArgs reads toolbox [OPTIONS] run [OPTIONS] COMMAND: run is found
// as cobra finds a subcommand, and its options and those of toolbox end at
// the first word that is no option. toolbox enter [CONTAINER] takes no
// command: it is marked as an option, for attach.
func toolboxArgs(args []string) ([]option, int) {
	s := cobraSub(args, toolboxBools, "run", "enter")
	if s < len(args) && args[s] == "enter" {
		opts, _ := toolboxOpts.read(args[:s])
		more, _, _ := readAll(toolboxOpts, args, s+1)
		return append(append(opts, more...), option{name: "enter", word: s}), len(args)
	}
	if s == len(args) || args[s] != "run" {
		return nil, len(args)
	}
	opts, _ := toolboxOpts.read(args[:s])
	more, cmd := readFrom(toolboxOpts, args, s+1)
	return append(opts, more...), cmd
}

// distroboxNone are the options with which distrobox-enter runs nothing:
// -h and -V print, --dry-run prints the command of the container, and "?"
// stands for an option it fails on.
var distroboxNone = []string{"h", "help", "V", "version", "d", "dry-run", "?"}

// distroboxEnter reads the words of distrobox-enter from i on as its loop
// does: an option is a word of its own, --name and --additional-flags take
// the next one, a word that is no option names the container, and the words
// after -e, --exec or "--" are the command. With none it runs a login shell
// in the container.
func distroboxEnter(args []string, i int) ([]option, int) {
	var opts []option
	for ; i < len(args); i++ {
		switch a := args[i]; a {
		case "-e", "--exec", "--":
			return opts, i + 1
		case "-n", "--name", "-a", "--additional-flags":
			if i+1 == len(args) || args[i+1] == "" {
				// distrobox-enter loops on it for ever.
				return append(opts, option{name: "?", word: i}), len(args)
			}
			opts = append(opts, option{name: strings.TrimLeft(a, "-"), value: args[i+1], word: i + 1})
			i++
		case "":
			// It ends the options, with no command of the user's.
			return opts, len(args)
		case "-h", "--help", "-V", "--version", "-d", "--dry-run", "-v", "--verbose", "-T", "-H", "--no-tty",
			"-r", "--root", "-nw", "--no-workdir", "-Y", "--yes", "--clean-path":
			opts = append(opts, option{name: strings.TrimLeft(a, "-"), word: i})
		default:
			if strings.HasPrefix(a, "-") {
				return append(opts, option{name: "?", word: i}), len(args)
			}
		}
	}
	return opts, len(args)
}

// distroboxCheck marks the flags --additional-flags adds to the command of
// the container manager, which its words may make any command, and --root,
// which runs the manager by sudo or the program set for it.
func distroboxCheck(p *parser, opts []option, _ []string, _ []bool, _ int) []string {
	if has(opts, "a", "additional-flags", "r", "root") {
		p.mark(dynComputed)
	}
	return nil
}

// machinectlOpts are the options of machinectl.
var machinectlOpts = getopt{short: "+hp:P:als:n:o:qE:VH:M:", long: "help version host: machine: system user value property: all full kill-whom: signal: uid: setenv: read-only mkdir lines: max-addresses: output: force now runner: verify: format: no-pager no-legend no-ask-password quiet"}

// machinectlArgs reads machinectl shell [[USER@]NAME [PATH [ARGS…]]]: the
// options come before the verb and among its first operands, the program
// after the machine. The verb is marked as an option, for attach.
func machinectlArgs(args []string) ([]option, int) {
	opts, i := readFrom(machinectlOpts, args, 0)
	if i == len(args) || args[i] != "shell" {
		return opts, len(args)
	}
	opts = append(opts, option{name: "shell", word: i})
	more, m := readFrom(machinectlOpts, args, i+1)
	opts = append(opts, more...)
	if m == len(args) {
		return opts, m
	}
	more, cmd := readFrom(machinectlOpts, args, m+1)
	return append(opts, more...), cmd
}

// kubectlOpts are the options of kubectl exec and those of kubectl, which
// it reads too; kubectlBools are those that take no value.
var (
	kubectlOpts  = getopt{short: "c:f:iqtn:s:v:h", long: "container: filename: pod-running-timeout: quiet stdin tty as: as-group: as-uid: as-user-extra: cache-dir: certificate-authority: client-certificate: client-key: cluster: context: disable-compression insecure-skip-tls-verify kubeconfig: kuberc: log-flush-frequency: match-server-version namespace: password: profile: profile-output: request-timeout: server: tls-server-name: token: user: username: v: vmodule: warnings-as-errors help"}
	kubectlBools = []string{"disable-compression", "insecure-skip-tls-verify", "match-server-version", "warnings-as-errors", "h", "help"}
)

// kubectlArgs reads kubectl [OPTIONS] exec [OPTIONS] POD -- COMMAND, with
// the options among the operands: the command is the words after "--", or,
// as kubectl before 1.31 had it, the operands after the pod. The other
// subcommands that run one are read by kube.
func kubectlArgs(args []string) ([]option, int) {
	s := cobraSub(args, kubectlBools, kubeSubs...)
	if s == len(args) || args[s] != "exec" {
		return nil, len(args)
	}
	pre, _ := kubectlOpts.read(args[:s])
	opts, ops, dash := readAll(kubectlOpts, args, s+1)
	opts = append(pre, opts...)
	switch {
	case has(opts, "h", "help"):
		return opts, len(args)
	case dash >= 0:
		return opts, dash + 1
	case len(ops) < 2:
		return opts, len(args)
	}
	return opts, ops[1]
}

// kubectlCheck marks an option of table kubectl does not know, and one among
// the words of the command, which kubectl takes out of them.
func kubectlCheck(p *parser, table getopt, opts []option, cmd int) {
	p.strange(table, opts)
	if slices.ContainsFunc(opts, func(o option) bool { return o.word > cmd }) {
		p.mark(dynComputed)
	}
}

// lxcOpts are the options of lxc exec and incus exec and those of lxc and
// incus, which they read too; lxcBools are those that take no value.
var (
	lxcOpts  = getopt{short: "ntTqvh", long: "env: mode: force-interactive force-noninteractive disable-stdin user: group: cwd: version help force-local project: debug verbose quiet sub-commands explain"}
	lxcBools = []string{"version", "h", "help", "force-local", "debug", "v", "verbose", "q", "quiet", "sub-commands", "explain"}
)

// lxcWrapper is lxc and incus: exec [REMOTE:]INSTANCE [--] COMMAND, with the
// options among the operands, and shell INSTANCE, the alias of exec
// INSTANCE -- su -l, marked as an option for attach.
var lxcWrapper = wrapper{
	opts: lxcOpts,
	reads: func(args []string) ([]option, int) {
		s := cobraSub(args, lxcBools, "exec", "shell")
		if s == len(args) || args[s] != "exec" && args[s] != "shell" {
			return nil, len(args)
		}
		pre, _ := lxcOpts.read(args[:s])
		opts, ops, _ := readAll(lxcOpts, args, s+1)
		opts = append(pre, opts...)
		if args[s] == "shell" {
			return append(opts, option{name: "shell", word: s}), len(args)
		}
		if len(ops) < 2 {
			return opts, len(args)
		}
		return opts, ops[1]
	},
	none: []string{"h", "help", "version", "explain"}, bare: true, attach: []string{"shell"},
	check: func(p *parser, opts []option, args []string, static []bool, cmd int) []string {
		p.strange(lxcOpts, opts, "shell")
		if slices.ContainsFunc(opts, func(o option) bool { return o.word > cmd }) {
			p.mark(dynComputed)
		}
		return envCheck("env")(p, opts, args, static, cmd)
	},
}

// boxCLI is how docker, podman or nerdctl reads its words up to the command
// of a container: its own options, the subcommand (exec, run or create,
// alone or after container), the options of that, then the container of
// exec or the image of run and create, and the command after it. attach,
// and start with -i, hand stdin to the process of the container, which may
// be a shell: attach is marked as -i. compose reads the rest (see
// composeRead).
type boxCLI struct {
	// global are the options of the program. With cobra they are found as
	// cobra's Traverse finds them (see cobraSub), bools taking no value, and
	// are those of the subcommands too; else they come before it, as getopt
	// reads them.
	global getopt
	cobra  bool
	bools  []string
	// exec and run are the options of exec and of run and create, attach
	// and start those of attach and start.
	exec, run, attach, start getopt
	// json tells that an --entrypoint of a JSON array is its words, multi
	// that the values of all the --entrypoint options are.
	json, multi bool
	// programs are the options whose value is a program the engine runs,
	// opaque those whose files hold code or variables, configs those that
	// name the config of the client, which names programs it runs: the
	// helpers of credsStore and credHelpers, the directories of plugins.
	programs, opaque, configs []string
}

// boxRead is what boxCLI reads of a line: the options, the index of the
// command (len(args) for none), that of the container or the image (-1
// for none) and the entrypoint, nil for that of the image.
type boxRead struct {
	opts  []option
	cmd   int
	image int
	entry []string
}

// boxSubs are the subcommands boxCLI reads further.
var boxSubs = []string{"exec", "run", "create", "attach", "start", "compose"}

func (c boxCLI) parse(args []string) boxRead {
	r := boxRead{cmd: len(args), image: -1}
	var i int
	if c.cobra {
		i = cobraSub(args, c.bools, append([]string{"container"}, boxSubs...)...)
		r.opts, _ = c.global.read(args[:i])
	} else {
		r.opts, i = readFrom(c.global, args, 0)
	}
	if has(r.opts, "h", "help", "v", "version") {
		return r
	}
	if i < len(args) && args[i] == "container" {
		i++
		if c.cobra {
			s := i + cobraSub(args[i:], c.bools, boxSubs...)
			more, _, _ := readAll(c.global, args[:s], i)
			r.opts, i = append(r.opts, more...), s
		}
	} else if i < len(args) && args[i] == "compose" {
		cr := composeRead(args, i+1)
		cr.opts = append(r.opts, cr.opts...)
		return cr
	}
	if i == len(args) {
		return r
	}
	switch args[i] {
	case "attach":
		more, _, _ := readAll(c.attach, args, i+1)
		r.opts = append(r.opts, more...)
		if !has(more, "no-stdin", "h", "help") {
			r.opts = append(r.opts, option{name: "interactive", word: i})
		}
	case "start":
		if more, _, _ := readAll(c.start, args, i+1); !has(more, "h", "help") {
			r.opts = append(r.opts, more...)
		}
	case "exec":
		more, ctr := readFrom(c.exec, args, i+1)
		r.opts = append(r.opts, more...)
		switch {
		case has(more, "h", "help"):
			return r
		case has(more, "l", "latest", "cidfile"):
			// podman exec --latest takes no container.
			r.cmd = ctr
		default:
			r.image, r.cmd = ctr, min(ctr+1, len(args))
		}
	case "run", "create":
		more, img := readFrom(c.run, args, i+1)
		r.opts = append(r.opts, more...)
		if has(more, "help") || img == len(args) {
			return r
		}
		r.image, r.cmd = img, img+1
		if r.entry, _ = c.entry(more, nil); r.entry != nil {
			// It runs with the words after the image, in its place.
			r.cmd = img
		}
	}
	return r
}

// entry is what the --entrypoint options among opts run in place of the
// image, nil for the entrypoint of the image, and whether it is static by
// static, nil for none.
func (c boxCLI) entry(opts []option, static []bool) ([]string, bool) {
	var words []string
	st := true
	for _, o := range slices.Backward(opts) {
		if o.name != "entrypoint" {
			continue
		}
		st = st && static != nil && static[o.word]
		if !c.multi {
			return c.entrypoint(o.value), st
		}
		words = append([]string{o.value}, words...)
	}
	return words, st
}

// entrypoint is the program and arguments of --entrypoint VALUE, nil for
// one that leaves the image without one: "" or [].
func (c boxCLI) entrypoint(v string) []string {
	var words []string
	if c.json && json.Unmarshal([]byte(v), &words) == nil {
		if len(words) == 0 {
			return nil
		}
		return words
	}
	if v == "" {
		return nil
	}
	return []string{v}
}

// tables are all the options of c, for fixed and strange: a letter means
// in the first table that has it what it means to run and create.
func (c boxCLI) tables() getopt {
	return getopt{
		short: c.run.short + c.exec.short + c.attach.short + c.start.short + c.global.short,
		long:  c.run.long + " " + c.exec.long + " " + c.attach.long + " " + c.start.long + " " + c.global.long,
	}
}

func (c boxCLI) wrapper() wrapper {
	return wrapper{
		opts: c.tables(),
		reads: func(args []string) ([]option, int) {
			r := c.parse(args)
			return r.opts, r.cmd
		},
		prog: func(opts []option, static []bool) ([]string, bool) {
			if k := slices.IndexFunc(opts, composed); k >= 0 {
				return composeEntry(opts[k+1:], static)
			}
			return c.entry(opts, static)
		},
		bare: true, attach: []string{"i", "interactive"}, check: c.check,
	}
}

// check marks an option the tables do not hold, the variables of -e and
// --env (and --env-merge of podman), those of --env-file and the files of
// opaque, which may be any, and a config of configs, which names programs;
// it returns the program of programs and the code of --health-cmd, which
// runs with sh -c in the container: one of JSON or led by CMD or NONE,
// which podman runs otherwise, is marked, and so is a --log-driver of a
// URI, a program nerdctl runs for the logs. An image made at run time may
// move the words after it. The options after compose are its own (see
// composeCheck).
func (c boxCLI) check(p *parser, opts []option, args []string, static []bool, _ int) []string {
	r := c.parse(args)
	var code []string
	if k := slices.IndexFunc(opts, composed); k >= 0 {
		code = composeCheck(p, opts[k:], args, static)
		opts = opts[:k]
	}
	p.strange(c.tables(), opts)
	for _, o := range opts {
		switch o.name {
		case "e", "env", "env-merge":
			p.setenv(o.value, static[o.word])
		case "env-file":
			p.mark(dynComputed)
		case "health-cmd", "health-startup-cmd", "healthcheck-command":
			if static[o.word] && !healthForm(o.value) {
				code = append(code, o.value)
			} else {
				p.mark(dynComputed)
			}
		case "log-driver":
			if strings.Contains(o.value, "://") || !static[o.word] {
				p.mark(dynComputed)
			}
		}
		switch {
		case slices.Contains(c.configs, o.name):
			p.mark(dynRebind)
		case slices.Contains(c.opaque, o.name), slices.Contains(c.programs, o.name) && !static[o.word]:
			p.mark(dynComputed)
		case slices.Contains(c.programs, o.name):
			code = append(code, escaped([]string{o.value}))
		}
	}
	if r.entry != nil && !static[r.image] {
		p.mark(dynComputed)
	}
	return code
}

// healthForm tells whether a --health-cmd is not one string for sh -c: a
// JSON array, or led by CMD, CMD-SHELL or NONE.
func healthForm(v string) bool {
	v = strings.TrimSpace(v)
	first, _, _ := strings.Cut(v, " ")
	switch strings.ToUpper(first) {
	case "CMD", "CMD-SHELL", "NONE":
		return true
	}
	return strings.HasPrefix(v, "[")
}

// dockerCLI is docker: its own options come before the subcommand, as
// pflag reads them; run and create share their options.
var dockerCLI = boxCLI{
	global:  getopt{short: "+c:DH:l:hv", long: "config: context: debug host: log-level: tls tlscacert: tlscert: tlskey: tlsverify version help"},
	exec:    getopt{short: "+de:ihtu:w:", long: "detach detach-keys: env: env-file: interactive privileged tty user: workdir: help"},
	attach:  getopt{short: "h", long: "detach-keys: no-stdin sig-proxy help"},
	start:   getopt{short: "aih", long: "attach checkpoint: checkpoint-dir: detach-keys: interactive help"},
	configs: []string{"config"},
	run: getopt{
		short: "+a:c:de:h:il:m:p:Pqtu:v:w:",
		long:  "add-host: annotation: attach: blkio-weight: blkio-weight-device: cap-add: cap-drop: cgroup-parent: cgroupns: cidfile: cpu-count: cpu-percent: cpu-period: cpu-quota: cpu-rt-period: cpu-rt-runtime: cpu-shares: cpus: cpuset-cpus: cpuset-mems: detach detach-keys: device: device-cgroup-rule: device-read-bps: device-read-iops: device-write-bps: device-write-iops: disable-content-trust dns: dns-opt: dns-option: dns-search: domainname: entrypoint: env: env-file: expose: gpus: group-add: health-cmd: health-interval: health-retries: health-start-interval: health-start-period: health-timeout: help hostname: init interactive io-maxbandwidth: io-maxiops: ip: ip6: ipc: isolation: kernel-memory: label: label-file: link: link-local-ip: log-driver: log-opt: mac-address: memory: memory-reservation: memory-swap: memory-swappiness: mount: name: net: net-alias: network: network-alias: no-healthcheck oom-kill-disable oom-score-adj: pid: pids-limit: platform: privileged publish: publish-all pull: quiet read-only restart: rm runtime: security-opt: shm-size: sig-proxy stop-signal: stop-timeout: storage-opt: sysctl: tmpfs: tty ulimit: use-api-socket user: userns: uts: volume: volume-driver: volumes-from: workdir:",
	},
}

// podmanGlobal are the options of podman that its subcommands read too,
// podmanCLI is podman, which finds its subcommand as cobra does: its own
// options run the OCI runtime and conmon of --runtime and --conmon, and
// take hooks and settings from --hooks-dir, --cdi-spec-dir, whose specs hold
// hooks too, and --module.
var (
	podmanGlobal = " cgroup-manager: cpu-profile: memory-profile: conmon: network-config-dir: default-mounts-file: events-backend: hooks-dir: cdi-spec-dir: max-workers: namespace: network-backend: root: registries-conf: runroot: imagestore: transient-store pull-option: runtime: storage-driver: tmpdir: trace volumepath: storage-opt: help log-level: runtime-flag: syslog"
	podmanCLI    = boxCLI{
		global: getopt{short: "c:H:rDv", long: "ssh: connection: url: host: config: context: identity: tls-cert: tls-key: tls-ca: tls-details: out: noout remote module: debug version" + podmanGlobal},
		cobra:  true,
		bools:  []string{"r", "remote", "D", "debug", "v", "version", "noout", "transient-store", "trace", "help", "syslog"},
		exec:   getopt{short: "+de:iltu:w:", long: "cidfile: detach detach-keys: env: env-file: interactive latest no-session preserve-fd: preserve-fds: privileged tty user: workdir: wait:" + podmanGlobal},
		run: getopt{
			short: "+a:c:de:h:il:m:p:Pqtu:v:w:",
			long:  "add-host: annotation: arch: attach: authfile: blkio-weight: blkio-weight-device: cap-add: cap-drop: cert-dir: cgroup-conf: cgroup-parent: cgroupns: cgroups: chrootdirs: cidfile: conmon-pidfile: cpu-period: cpu-quota: cpu-rt-period: cpu-rt-runtime: cpu-shares: cpus: cpuset-cpus: cpuset-mems: creds: decryption-key: detach detach-keys: device: device-cgroup-rule: device-read-bps: device-read-iops: device-write-bps: device-write-iops: disable-content-trust dns: dns-opt: dns-option: dns-search: entrypoint: env: env-file: env-host env-merge: expose: gidmap: gpus: group-add: group-entry: health-cmd: health-interval: health-log-destination: health-max-log-count: health-max-log-size: health-on-failure: health-retries: health-start-period: health-startup-cmd: health-startup-interval: health-startup-retries: health-startup-success: health-startup-timeout: health-timeout: healthcheck-command: healthcheck-interval: healthcheck-retries: healthcheck-start-period: healthcheck-timeout: hostname: hosts-file: hostuser: http-proxy image-volume: init init-path: interactive ip: ip6: ipc: kernel-memory: label: label-file: log-driver: log-opt: mac-address: memory: memory-reservation: memory-swap: memory-swappiness: mount: name: net: network: network-alias: no-healthcheck no-hostname no-hosts oom-kill-disable oom-score-adj: os: override-arch: override-os: override-variant: passwd passwd-entry: personality: pid: pidfile: pids-limit: platform: pod: pod-id-file: preserve-fd: preserve-fds: privileged publish: publish-all pull: quiet rdt-class: read-only read-only-tmpfs replace requires: restart: retry: retry-delay: rm rmi rootfs sdnotify: seccomp-policy: secret: security-opt: shm-size: shm-size-systemd: sig-proxy signature-policy: stop-signal: stop-timeout: subgidname: subuidname: sysctl: systemd: timeout: tls-verify tmpfs: tty tz: uidmap: ulimit: umask: unsetenv: unsetenv-all user: userns: uts: variant: volume: volumes-from: workdir:" + podmanGlobal,
		},
		attach:   getopt{short: "hl", long: "detach-keys: no-stdin sig-proxy latest" + podmanGlobal},
		start:    getopt{short: "af:hil", long: "all attach detach-keys: filter: interactive latest sig-proxy" + podmanGlobal},
		json:     true,
		programs: []string{"conmon", "runtime"},
		opaque:   []string{"hooks-dir", "module", "cdi-spec-dir"},
		configs:  []string{"config"},
	}
)
