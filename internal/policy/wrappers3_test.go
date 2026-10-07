package policy

import (
	"context"
	"slices"
	"testing"
)

// The lines of the wrappers of boxWrappers that ran sudo unseen: the rules
// and Cedar see it now, in a container as here.
var boxSudo = []string{
	`capsh -- -c 'sudo ls'`, `capsh --user=me --shell=/bin/sh -- -c 'sudo ls'`, `capsh == -- -c 'sudo ls'`,
	`ip netns exec NS sudo ls`, `ip vrf exec VRF sudo ls`, `ip -all netns exec sudo ls`, `ip -n x net e NS sudo ls`,
	`bwrap --bind / / sudo ls`, `bwrap --ro-bind / / --setenv A 1 --overlay a b c -- sudo ls`,
	`systemd-nspawn -D / sudo ls`, `systemd-nspawn -q --bind=/x -u root -D /srv sudo ls`,
	`dbus-run-session sudo ls`, `dbus-run-session --config-file x -- sudo ls`,
	`ssh-agent sudo ls`, `ssh-agent -a sock -t 60 sudo ls`, `eatmydata sudo ls`, `eatmydata -- sudo ls`,
	`flatpak-spawn --host sudo ls`, `flatpak-spawn --host --watch-bus --directory=/tmp sudo ls`,
	`toolbox run sudo ls`, `toolbox --log-level debug run -c box sudo ls`,
	`distrobox enter x -- sudo ls`, `distrobox enter -n x -e sudo ls`, `distrobox-enter x -- sudo ls`,
	`docker exec c sudo ls`, `docker exec -it -u root -w /tmp c sudo ls`, `docker run IMG sudo ls`,
	`docker run --rm -it -v /:/h -e A=1 --name x IMG sudo ls`, `docker -H unix:///x run IMG sudo ls`,
	`docker container exec c sudo ls`, `docker create IMG sudo ls`, `docker run --entrypoint sudo IMG ls`,
	`docker run --entrypoint=sudo IMG`, `docker run --health-cmd 'sudo ls' IMG`,
	`podman exec c sudo ls`, `podman run IMG sudo ls`, `podman --log-level debug container run IMG sudo ls`,
	`podman exec -l sudo ls`, `podman run --entrypoint '["sudo","ls"]' IMG`,
	`kubectl exec POD -- sudo ls`, `kubectl -n ns exec -it POD -c ctr -- sudo ls`, `kubectl exec POD sudo ls`,
	`lxc exec C -- sudo ls`, `lxc --project p exec remote:C --env A=1 -- sudo ls`, `incus exec C -- sudo ls`,
	`machinectl shell M /bin/sudo ls`, `machinectl shell root@ /usr/bin/sudo ls`, `machinectl -q shell -E A=1 M /bin/sudo ls`,
}

// The wrappers of boxWrappers run the command after their words, read as
// the program reads them: the rules and Cedar see it.
func TestParseBoxWrappers(t *testing.T) {
	for _, src := range boxSudo {
		s, err := Parse(src, "", "")
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if !slices.ContainsFunc(s.Commands, func(a []string) bool {
			return len(a) > 0 && (a[0] == "sudo" || a[0] == "/bin/sudo" || a[0] == "/usr/bin/sudo")
		}) {
			t.Errorf("%s: sudo not among the commands %q", src, s.Commands)
		}
		if s.Remote != nil {
			t.Errorf("%s: remote %v", src, s.Remote)
		}
	}
	// What the wrapper reads runs, in place of the word of its command.
	for src, want := range map[string][]string{
		`capsh -- -c 'sudo ls'`:                      {"/bin/bash", "-c", "sudo ls"},
		`capsh --shell=/bin/zsh == -- -c id`:         {"/bin/bash", "-c", "id"},
		`capsh == --shell=/bin/zsh -+ -c id`:         {"/bin/zsh", "-c", "id"},
		`docker run --entrypoint sudo IMG ls -l`:     {"sudo", "ls", "-l"},
		`docker run --entrypoint '' IMG ls -l`:       {"ls", "-l"},
		`podman run --entrypoint '["env","-i"]' I x`: {"env", "-i", "x"},
		`podman run --entrypoint '[]' IMG ls`:        {"ls"},
		`podman exec --cidfile f ls -l`:              {"ls", "-l"},
		`ip -all netns exec ls`:                      {"ls"},
		`ip -all vrf exec V ls`:                      {"ls"},
		`kubectl exec POD extra -- ls -l`:            {"ls", "-l"},
		`dbus-run-session --dbus-daemon=/tmp/d true`: {"/tmp/d"},
		`podman --runtime /tmp/r run IMG true`:       {"/tmp/r"},
	} {
		if s, _ := Parse(src, "", ""); !hasCommand(s, want) {
			t.Errorf("%s: %q not among the commands %q", src, want, s.Commands)
		}
	}
	// Options, their values, the container and the image are no command; a
	// subcommand that runs none, an option that prints and exits and one
	// that runs the command elsewhere leave the words alone.
	for src, not := range map[string][]string{
		`docker run -v /x:/y -e A=1 IMG ls`:          {"IMG", "ls"},
		`docker exec -u root c ls`:                   {"c", "ls"},
		`docker run --help IMG sudo ls`:              {"sudo", "ls"},
		`docker --version run IMG sudo ls`:           {"sudo", "ls"},
		`docker container --help exec c sudo ls`:     {"sudo", "ls"},
		`podman run -l x IMG ls`:                     {"IMG", "ls"},
		`kubectl -n ns exec POD -- ls`:               {"POD", "--", "ls"},
		`kubectl exec --help POD -- sudo ls`:         {"sudo", "ls"},
		`lxc list`:                                   {"list"},
		`ip ne exec NS sudo ls`:                      {"NS", "sudo", "ls"},
		`ip -b f netns exec NS sudo ls`:              {"sudo", "ls"},
		`ip -V netns exec NS sudo ls`:                {"sudo", "ls"},
		`ip netns exec NS sudo ls`:                   {"NS", "sudo", "ls"},
		`bwrap --help sudo ls`:                       {"sudo", "ls"},
		`bwrap --newopt x sudo ls`:                   {"sudo", "ls"},
		`bwrap --bind / / sudo ls`:                   {"/", "sudo", "ls"},
		`capsh --help -- -c 'sudo ls'`:               {"/bin/bash", "-c", "sudo ls"},
		`systemd-nspawn -b -D /srv sudo ls`:          {"sudo", "ls"},
		`ssh-agent -k sudo ls`:                       {"sudo", "ls"},
		`ssh-agent -s sudo ls`:                       {"sudo", "ls"},
		`distrobox enter --dry-run x -- sudo ls`:     {"sudo", "ls"},
		`distrobox enter --nope x -- sudo ls`:        {"sudo", "ls"},
		`distrobox list x -- sudo ls`:                {"sudo", "ls"},
		`machinectl list M /bin/sudo ls`:             {"/bin/sudo", "ls"},
		`flatpak-spawn --help sudo ls`:               {"sudo", "ls"},
		`toolbox list sudo ls`:                       {"sudo", "ls"},
		`dbus-run-session --help sudo ls`:            {"sudo", "ls"},
		`dbus-run-session --config-file x sudo ls`:   {"x", "sudo", "ls"},
		`machinectl shell -E A=1 M /bin/ls`:          {"A=1", "M", "/bin/ls"},
		`toolbox run --container box ls`:             {"box", "ls"},
		`flatpak-spawn --env=A=1 ls`:                 {"--env=A=1", "ls"},
		`systemd-nspawn --setenv A=1 -D /srv ls`:     {"A=1", "-D", "/srv", "ls"},
		`distrobox enter -n box -a '--privileged' x`: {"box"},
	} {
		if s, _ := Parse(src, "", ""); hasCommand(s, not) {
			t.Errorf("%s: %q among the commands %q", src, not, s.Commands)
		}
	}
}

// What a box wrapper starts with no command, and what it hands the command
// besides its words, is marked: a shell on stdin, variables of the
// environment, options from a descriptor or a file, an option the tables do
// not know, an option of kubectl or lxc among the words of the command,
// which they take out of them.
func TestParseBoxWrapperMarks(t *testing.T) {
	for _, c := range []struct {
		src     string
		dynamic []string
	}{
		{`capsh --`, []string{"stdin"}},
		{`echo id | capsh --print --`, []string{"stdin"}},
		{`bwrap --bind / / bash`, []string{"stdin"}},
		{`systemd-nspawn -D /srv`, []string{"stdin"}},
		{`echo id | docker run -i IMG`, []string{"stdin"}},
		{`echo id | podman run --interactive IMG`, []string{"stdin"}},
		{`echo id | lxc shell C`, []string{"stdin"}},
		{`echo id | machinectl shell M`, []string{"stdin"}},
		{`echo id | machinectl shell`, []string{"stdin"}},
		{`echo id | distrobox enter x`, []string{"stdin"}},
		{`docker exec -i c bash`, []string{"stdin"}},
		{`kubectl exec -i POD -- bash`, []string{"stdin"}},
		{`ssh-agent bash`, []string{"stdin"}},

		{`docker run IMG`, nil},
		{`docker run -i --entrypoint ls IMG`, nil},
		{`systemd-nspawn -b -D /srv`, nil},
		{`machinectl list`, nil},
		{`lxc list`, nil},
		{`distrobox list`, nil},
		{`capsh --print`, nil},

		{`docker run -e PATH=/tmp IMG ls`, []string{"rebind"}},
		{`docker exec --env LD_PRELOAD=/x.so c ls`, []string{"rebind"}},
		{`podman run --env-merge 'PATH=${PATH}:/x' IMG ls`, []string{"rebind"}},
		{`docker run -e PAGER='sudo ls' IMG man ls`, nil},
		{`docker run --env-file f IMG ls`, []string{"computed"}},
		{`lxc exec --env PATH=/tmp C -- ls`, []string{"rebind"}},
		{`machinectl shell -E PATH=/tmp M /bin/ls`, []string{"rebind"}},
		{`systemd-nspawn -E PATH=/tmp -D /srv ls`, []string{"rebind"}},
		{`bwrap --setenv PATH /tmp --bind / / ls`, []string{"rebind"}},
		{`bwrap --args 3 ls`, []string{"computed"}},
		{`flatpak-spawn --host --env=PATH=/tmp ls`, []string{"rebind"}},
		{`flatpak-spawn --host --env-fd=3 ls`, []string{"computed"}},
		{`flatpak-spawn --usr-path=/tmp/usr ls`, []string{"rebind"}},
		{`distrobox enter -a '--env X=1' x -- ls`, []string{"computed"}},
		{`distrobox enter -r x -- ls`, []string{"computed"}},
		{`podman --hooks-dir /x run IMG ls`, []string{"computed"}},
		{`podman --conmon "$c" run IMG ls`, []string{"computed"}},
		{`docker run --health-cmd '["CMD","ls"]' IMG`, []string{"computed"}},
		{`docker run --health-cmd "$h" IMG`, []string{"computed"}},
		{`docker run --entrypoint sudo "$img" ls`, []string{"computed"}},
		{`docker run --entrypoint "$e" IMG ls`, []string{"computed"}},
		{`docker run --newflag x IMG ls`, []string{"computed"}},
		{`kubectl --newflag exec POD -- ls`, []string{"computed"}},
		{`kubectl exec POD nice -c x sudo ls`, []string{"computed"}},
		{`lxc exec C nice --cwd /tmp sudo ls`, []string{"computed"}},
		{`docker exec "$c" ls`, []string{"computed"}},
		{`docker run -v $PWD:/x IMG ls`, []string{"computed"}},
		{`capsh --shell="$s" -- -c ls`, []string{"computed"}},
		{`xargs docker exec c`, []string{"computed"}},

		{`docker run --rm -v "$PWD:/x" -w /x IMG make`, nil},
		{`docker run --runtime nvidia IMG ls`, nil},
		{`kubectl -n "$ns" exec POD -- ls`, nil},
		{`kubectl --insecure-skip-tls-verify exec POD -- ls`, nil},
		{`docker run --health-cmd 'curl -f localhost' IMG`, nil},
	} {
		s, err := Parse(c.src, "", "")
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		if !slices.Equal(s.Dynamic, c.dynamic) {
			t.Errorf("%s: dynamic %q, want %q (commands %q)", c.src, s.Dynamic, c.dynamic, s.Commands)
		}
	}
}

// A line of these wrappers that runs no command holds no other command
// than its own, and nothing is marked.
func TestParseBoxWrappersNone(t *testing.T) {
	for _, src := range []string{
		`ip addr`, `ip -4 -o addr show`, `ip netns list`, `ip netns exec`, `ip route get 1.1.1.1`,
		`docker ps`, `docker images -a`, `docker logs -f c`, `docker build -t x .`, `docker run IMG`,
		`podman ps -a`, `kubectl get pods`, `kubectl exec POD`, `kubectl -n ns describe pod x`,
		`lxc list`, `incus info C`, `bwrap --help`, `bwrap --version`, `systemd-nspawn --help`,
		`capsh --print`, `capsh --help`, `ssh-agent -k`, `ssh-agent`, `eatmydata`, `dbus-run-session --version`,
		`toolbox list`, `distrobox list`, `distrobox enter --dry-run x -- ls`, `machinectl list`,
		`flatpak-spawn --help`,
	} {
		s, err := Parse(src, "/w", "/h")
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if len(s.Commands) != 1 || s.Dynamic != nil {
			t.Errorf("%s: commands %q, dynamic %q", src, s.Commands, s.Dynamic)
		}
	}
}

// The command of a container is one of this machine: it writes the files of
// its redirections, which a volume may share, here. Under another root, of
// bwrap, systemd-nspawn, capsh --chroot, podman --rootfs and the properties
// RootDirectory= and RootImage= of a unit, every path is another file.
func TestParseBoxWrapperPaths(t *testing.T) {
	for _, c := range []struct {
		src     string
		writes  []string
		dynamic []string
	}{
		{`docker exec c sh -c 'echo x > /etc/x'`, []string{"/etc/x"}, nil},
		{`kubectl exec POD -- sh -c 'echo x > out'`, []string{"/w/out"}, nil},
		{`docker run IMG sh -c 'cd /etc; echo x > passwd'`, nil, []string{"computed"}},
		{`docker exec c rm /etc/x`, nil, nil},
		{`bwrap --ro-bind / / ls`, nil, []string{"computed"}},
		{`bwrap --chdir /tmp --bind / / sh -c 'echo x > out'`, nil, []string{"computed"}},
		{`systemd-nspawn -D /srv rm /etc/x`, nil, []string{"computed"}},
		{`systemd-nspawn -D /srv sh -c 'echo x > out'`, nil, []string{"computed"}},
		{`capsh --chroot=/srv -- -c 'rm /etc/x'`, nil, []string{"computed"}},
		{`podman run --rootfs /srv rm /etc/x`, nil, []string{"computed"}},
		{`systemd-run -p RootDirectory=/srv rm /etc/x`, nil, []string{"computed"}},
		{`systemd-run --property=RootImage=/x.raw rm /etc/x`, nil, []string{"computed"}},
		{`run0 --property=RootDirectory=/srv rm /etc/x`, nil, []string{"computed"}},
		{`systemd-run -p Nice=5 rm /etc/x`, nil, nil},
		{`flatpak-spawn --host --directory=/etc sh -c 'echo x > passwd'`, nil, []string{"computed"}},
		{`ip netns exec NS sh -c 'echo x > out'`, []string{"/w/out"}, nil},
	} {
		s, err := Parse(c.src, "/w", "/h")
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		if !slices.Equal(s.Writes, c.writes) {
			t.Errorf("%s: writes %q, want %q", c.src, s.Writes, c.writes)
		}
		if !slices.Equal(s.Dynamic, c.dynamic) {
			t.Errorf("%s: dynamic %q, want %q", c.src, s.Dynamic, c.dynamic)
		}
		if s.Remote != nil {
			t.Errorf("%s: remote %v", c.src, s.Remote)
		}
	}
}

// nsenter -e takes the environment of the target, fakeroot -l puts its
// library in LD_PRELOAD: a name of a command may run another program.
func TestParseRebindWrappers(t *testing.T) {
	for _, c := range []struct {
		src     string
		dynamic []string
	}{
		{`nsenter -e -t 1 ls`, []string{"rebind"}},
		{`nsenter --env --target 1 -m ls`, []string{"rebind"}},
		{`nsenter -t 1 -m ls`, nil},
		{`fakeroot -l /tmp/x.so ls`, []string{"rebind"}},
		{`fakeroot --lib=/tmp/x.so ls`, []string{"rebind"}},
		{`fakeroot ls`, nil},
	} {
		s, err := Parse(c.src, "", "")
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		if !slices.Equal(s.Dynamic, c.dynamic) {
			t.Errorf("%s: dynamic %q, want %q", c.src, s.Dynamic, c.dynamic)
		}
	}
}

// With deny = ["sudo *"] the sudo behind every box wrapper is denied, and so
// it is by Cedar; what runs no sudo passes.
func TestBoxWrappersPolicy(t *testing.T) {
	ctx := context.Background()
	rules, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	cedar := mustLoad(t, map[string]string{"a.cedar": permitAll + `@reason("not this")
forbid(principal, action == Action::"run", resource == Command::"sudo");
`})
	for _, cmd := range boxSudo {
		for _, e := range []*Engine{rules, cedar} {
			if d := check(t, e, bash(cmd)); d.Action != Deny {
				t.Errorf("%s: %+v, want deny", cmd, d)
			}
		}
	}
	for _, c := range []struct{ cmd, want string }{
		{`ip addr`, Allow},
		{`docker ps`, Allow},
		{`kubectl get pods`, Allow},
		{`bwrap --help`, Allow},
		{`docker exec c ls`, Allow},
		{`kubectl exec POD -- ls`, Allow},
		{`capsh --`, Ask},
		{`bwrap --bind / / bash`, Ask},
		{`echo id | docker run -i IMG`, Ask},
		{`nsenter -e -t 1 ls`, Ask},
		{`systemd-run -p RootDirectory=/srv ls`, Ask},
		{`fakeroot -l /tmp/x.so ls`, Ask},
	} {
		if d := check(t, rules, bash(c.cmd)); d.Action != c.want {
			t.Errorf("%s: %+v, want %s", c.cmd, d, c.want)
		}
	}
}
