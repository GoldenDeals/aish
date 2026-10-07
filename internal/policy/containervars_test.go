package policy

import (
	"context"
	"slices"
	"testing"
)

// The DBX_ variables whose values distrobox evals or runs, however the line
// assigns them: their sudo is among the commands.
var dbxSudo = []string{
	`DBX_CONTAINER_NAME='$(sudo ls)' distrobox create`,
	`env DBX_CONTAINER_IMAGE='alpine;sudo ls' distrobox create -n x`,
	`export DBX_CONTAINER_HOSTNAME='$(sudo ls)'; distrobox create -n x`,
	`DBX_CONTAINER_CUSTOM_HOME='$(sudo ls)' distrobox-create -i alpine`,
	`DBX_CONTAINER_HOME_PREFIX='"; sudo ls; "' distrobox create`,
	"DBX_CONTAINER_NAME='`sudo ls`' distrobox create -i alpine",
	`DBX_CONTAINER_MANAGER='sudo podman' distrobox create -d`,
	`DBX_CONTAINER_MANAGER='sudo' distrobox list`,
	`DBX_SUDO_PROGRAM='sudo -n' distrobox create --root -n x`,
	`DBX_CONTAINER_IMAGE='alpine;sudo ls' distrobox ephemeral -- ls`,
	`DBX_CONTAINER_HOSTNAME='$(sudo ls)' distrobox enter box -- ls`,
	`declare -x DBX_CONTAINER_IMAGE='a $(sudo ls)'; distrobox assemble create`,
	`distrobox create -n x; export DBX_CONTAINER_NAME='$(sudo ls)'`,
}

func TestParseContainerVars(t *testing.T) {
	for _, src := range dbxSudo {
		s, err := Parse(src, "", "")
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if !slices.ContainsFunc(s.Commands, func(a []string) bool { return len(a) > 0 && a[0] == "sudo" }) {
			t.Errorf("%s: sudo not among the commands %q", src, s.Commands)
		}
	}
	for _, c := range []struct {
		src     string
		dynamic []string
	}{
		// What the value runs is not known.
		{`DBX_CONTAINER_NAME="$n" distrobox create`, []string{"computed"}},
		{`env DBX_CONTAINER_IMAGE="$i" distrobox create -n x`, []string{"computed"}},
		{`read -r DBX_CONTAINER_HOSTNAME; distrobox create`, []string{"computed"}},
		// Another config of the client, with the programs it names.
		{`CONTAINERS_CONF=/tmp/c podman run img`, []string{"rebind"}},
		{`CONTAINERS_CONF_OVERRIDE=/tmp/c podman run img`, []string{"rebind"}},
		{`export CONTAINERS_STORAGE_CONF=/tmp/s; podman run img`, []string{"rebind"}},
		{`CONTAINERS_HELPER_BINARY_DIR=/tmp/b podman run img`, []string{"rebind"}},
		{`NERDCTL_TOML=/tmp/n nerdctl run img`, []string{"rebind"}},
		{`env CNI_PATH=/tmp/x nerdctl run img`, []string{"rebind"}},
		{`NETCONFPATH=/tmp/x nerdctl run img`, []string{"rebind"}},
		{`XDG_CONFIG_HOME=/tmp/x distrobox create -n box`, []string{"rebind"}},
		{`XDG_CONFIG_HOME=/tmp/x git status`, []string{"rebind"}},

		{`distrobox create -n box -i alpine`, nil},
		{`podman run img`, nil},
		{`nerdctl run img`, nil},
		{`DBX_NON_INTERACTIVE=1 DBX_CONTAINER_NAME=test-alpine DBX_CONTAINER_IMAGE=alpine distrobox-create`, nil},
		{`DBX_CONTAINER_MANAGER=podman DBX_VERBOSE=1 distrobox create -n box`, nil},
		{`DBX_CONTAINER_MANAGER=autodetect distrobox list`, nil},
		{`DBX_CONTAINER_HOME_PREFIX=~/boxes distrobox create -n box -i alpine`, nil},
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
	// The manager the variable names is a command of the line, and the
	// default one none.
	for src, want := range map[string][]string{
		`DBX_CONTAINER_MANAGER=/opt/podman distrobox create -n box`: {"/opt/podman"},
		`DBX_CONTAINER_IMAGE=alpine distrobox create`:               {":", "alpine"},
		`DBX_CONTAINER_NAME='my box' distrobox create`:              {":", "my box"},
	} {
		if s, _ := Parse(src, "", ""); !hasCommand(s, want) {
			t.Errorf("%s: %q not among the commands %q", src, want, s.Commands)
		}
	}
	if s, _ := Parse(`DBX_CONTAINER_MANAGER=autodetect distrobox list`, "", ""); hasCommand(s, []string{"autodetect"}) {
		t.Errorf("autodetect among the commands %q", s.Commands)
	}
}

// With deny = ["sudo *"] the sudo in a DBX_ variable is denied, and so it is
// by Cedar; another config of podman or nerdctl asks, and the rest passes.
func TestContainerVarsPolicy(t *testing.T) {
	ctx := context.Background()
	rules, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	cedar := mustLoad(t, map[string]string{"a.cedar": permitAll + `@reason("not this")
forbid(principal, action == Action::"run", resource == Command::"sudo");
`})
	for _, cmd := range dbxSudo {
		for _, e := range []*Engine{rules, cedar} {
			if d := check(t, e, bash(cmd)); d.Action != Deny {
				t.Errorf("%s: %+v, want deny", cmd, d)
			}
		}
	}
	for _, c := range []struct{ cmd, want string }{
		{`CONTAINERS_CONF=/tmp/c podman run img`, Ask},
		{`CONTAINERS_STORAGE_CONF=/tmp/s podman run img`, Ask},
		{`NERDCTL_TOML=/tmp/n nerdctl run img`, Ask},
		{`DBX_CONTAINER_NAME="$n" distrobox create`, Ask},
		{`distrobox create -n box -i alpine`, Allow},
		{`podman run img`, Allow},
		{`nerdctl run img`, Allow},
		{`DBX_NON_INTERACTIVE=1 DBX_CONTAINER_NAME=test-alpine DBX_CONTAINER_IMAGE=alpine distrobox-create`, Allow},
	} {
		if d := check(t, rules, bash(c.cmd)); d.Action != c.want {
			t.Errorf("%s: %+v, want %s", c.cmd, d, c.want)
		}
	}
}
