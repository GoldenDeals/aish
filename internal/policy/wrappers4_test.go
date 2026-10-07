package policy

import (
	"context"
	"slices"
	"testing"
)

// The lines of compose, nerdctl, kubectl run and debug, oc and distrobox
// ephemeral that ran sudo unseen, and those whose shell got it on stdin:
// the rules and Cedar see it now.
var boxSudo4 = []string{
	`docker compose exec app sudo ls`, `docker compose exec -u root -w /tmp -T app sudo ls`,
	`docker compose -f c.yml -p x --profile p exec app sudo ls`, `docker compose run app sudo ls`,
	`docker compose run --rm -e A=1 -v /x:/y --name n app sudo ls`, `docker compose run --entrypoint sudo app ls`,
	`docker compose run --entrypoint 'sudo -n' app ls`, `docker --config /tmp/d compose exec app sudo ls`,
	`docker -H unix:///x compose exec app sudo ls`, `docker-compose exec app sudo ls`, `docker-compose run app sudo ls`,
	`podman compose exec app sudo ls`, `podman --log-level debug compose run app sudo ls`, `podman-compose exec app sudo ls`,
	`nerdctl compose exec app sudo ls`, `nerdctl compose -f c.yml run app sudo ls`,
	`nerdctl exec c sudo ls`, `nerdctl exec -it -u root -w /tmp c sudo ls`, `nerdctl -n k8s.io exec c sudo ls`,
	`nerdctl --namespace k8s.io --debug exec c sudo ls`, `nerdctl container exec c sudo ls`, `nerdctl run IMG sudo ls`,
	`nerdctl run --rm -it -v /:/h -h host --name x IMG sudo ls`, `nerdctl container run IMG sudo ls`,
	`nerdctl create IMG sudo ls`, `nerdctl run --entrypoint sudo IMG ls`, `nerdctl run --entrypoint sudo --entrypoint -n IMG ls`,
	`nerdctl run --health-cmd 'sudo ls' IMG`, `nerdctl run --init-binary sudo IMG ls`,
	`oc exec p -- sudo ls`, `oc -n ns exec -it p -c ctr -- sudo ls`, `oc rsh p sudo ls`, `oc rsh -c ctr -T p sudo ls`,
	`oc rsh -f pod.yaml sudo ls`, `oc rsh --shell /usr/bin/sudo p`, `oc debug node/n -- sudo ls`,
	`oc debug p --image=i A=1 -- sudo ls`, `oc run x --image=i -- sudo ls`,
	`kubectl run x --image=i -- sudo ls`, `kubectl run x --image=i --command -- sudo ls`,
	`kubectl run -it --rm x --image=i --restart=Never -- sudo ls`, `kubectl run x --image=i sudo ls`,
	`kubectl run x --dry-run --image=i -- sudo ls`, `kubectl debug p -it --image=i -- sudo ls`,
	`kubectl debug node/n --image=i -- sudo ls`, `kubectl -n ns debug p --image=i --target=c --profile=sysadmin -- sudo ls`,
	`distrobox ephemeral -- sudo ls`, `distrobox ephemeral -e sudo ls`, `distrobox ephemeral --exec sudo ls`,
	`distrobox ephemeral --image alpine -n box -- sudo ls`, `distrobox-ephemeral -v -- sudo ls`,
	`distrobox ephemeral -- 'sudo ls'`, `distrobox ephemeral --init-hooks 'sudo ls'`,
	`distrobox ephemeral --pre-init-hooks 'sudo ls' -- id`, `distrobox ephemeral '$(sudo ls)'`,
	`distrobox ephemeral ';sudo ls;'`, `distrobox ephemeral -ap '$(sudo ls)'`, `distrobox ephemeral -n '$(sudo ls)'`,
	`docker attach c <<< 'sudo ls'`, `docker container attach c <<< 'sudo ls'`, `docker start -ai c <<< 'sudo ls'`,
	`podman attach -l <<< 'sudo ls'`, `nerdctl start -i c <<< 'sudo ls'`, `toolbox enter <<< 'sudo ls'`,
	`kubectl attach -i p <<< 'sudo ls'`, `kubectl run -i x --image=i <<< 'sudo ls'`, `oc rsh p <<< 'sudo ls'`,
	`oc debug node/n <<< 'sudo ls'`, `docker compose run app <<< 'sudo ls'`, `docker compose attach app <<< 'sudo ls'`,
	`distrobox ephemeral <<< 'sudo ls'`,
}

func TestParseBoxWrappers4(t *testing.T) {
	for _, src := range boxSudo4 {
		s, err := Parse(src, "", "")
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if !slices.ContainsFunc(s.Commands, func(a []string) bool {
			return len(a) > 0 && (a[0] == "sudo" || a[0] == "/usr/bin/sudo")
		}) {
			t.Errorf("%s: sudo not among the commands %q", src, s.Commands)
		}
		if s.Remote != nil {
			t.Errorf("%s: remote %v", src, s.Remote)
		}
	}
	// What the wrapper runs, in place of the word of its command.
	for src, want := range map[string][]string{
		`docker compose run --entrypoint 'sudo -n' app ls -l`:  {"sudo", "-n", "ls", "-l"},
		`docker compose run --entrypoint sudo app`:             {"sudo"},
		`docker compose run --entrypoint '' app ls`:            {"ls"},
		`nerdctl run --entrypoint sudo --entrypoint -n IMG ls`: {"sudo", "-n", "ls"},
		`nerdctl run --runtime /tmp/r IMG true`:                {"/tmp/r"},
		`distrobox ephemeral -- 'sudo ls' -l`:                  {"sudo", "ls", "-l"},
		`distrobox ephemeral --image alpine -- ls`: {
			"distrobox-create", "--init-hooks", " ", "--pre-init-hooks", " ", "--image", "alpine",
			"--yes", "--name", "distrobox-XXXXXXXXXX",
		},
		`oc rsh p ls -l`:                     {"ls", "-l"},
		`oc rsh -f pod.yaml ls -l`:           {"ls", "-l"},
		`oc rsh --shell /bin/zsh p`:          {"/bin/zsh"},
		`kubectl run x --image=i -- ls -l`:   {"ls", "-l"},
		`kubectl debug p --image=i -- ls -l`: {"ls", "-l"},
		`oc debug p A=1 -- ls -l`:            {"ls", "-l"},
	} {
		if s, _ := Parse(src, "", ""); !hasCommand(s, want) {
			t.Errorf("%s: %q not among the commands %q", src, want, s.Commands)
		}
	}
	// The service, the container, the pod and the options are no command;
	// help, version and an option that runs nothing leave the words alone.
	for src, not := range map[string][]string{
		`docker compose exec -u root app ls`:           {"app", "ls"},
		`docker compose run -v /x:/y app ls`:           {"app", "ls"},
		`docker compose --help exec app sudo ls`:       {"sudo", "ls"},
		`docker compose exec --help app sudo ls`:       {"sudo", "ls"},
		`docker compose ps app sudo ls`:                {"sudo", "ls"},
		`nerdctl exec -u root c ls`:                    {"c", "ls"},
		`nerdctl -n ns exec c ls`:                      {"ns", "c", "ls"},
		`nerdctl run --help IMG sudo ls`:               {"sudo", "ls"},
		`kubectl run x --image=i`:                      {"x"},
		`kubectl debug p --image=i sudo ls`:            {"sudo", "ls"},
		`kubectl run --help x --image=i -- sudo ls`:    {"sudo", "ls"},
		`kubectl rsh p sudo ls`:                        {"sudo", "ls"},
		`oc rsh p`:                                     {"p"},
		`distrobox ephemeral --help -- sudo ls`:        {"sudo", "ls"},
		`distrobox ephemeral -n '' -- sudo ls`:         {"sudo", "ls"},
		`distrobox ephemeral -V '$(sudo ls)'`:          {"sudo", "ls"},
		`ip -b - netns exec NS sudo ls`:                {"sudo", "ls"},
		`toolbox enter sudo ls`:                        {"sudo", "ls"},
		`docker attach c sudo ls`:                      {"sudo", "ls"},
		`docker start -ai c sudo ls`:                   {"sudo", "ls"},
		`docker compose attach --no-stdin app sudo`:    {"sudo"},
		`distrobox ephemeral -- 'sudo ls' -l <<< 'id'`: {"id"},
	} {
		if s, _ := Parse(src, "", ""); hasCommand(s, not) {
			t.Errorf("%s: %q among the commands %q", src, not, s.Commands)
		}
	}
}

// What these wrappers start with no command, the files their clients load
// code from and what they hand the command besides its words is marked.
func TestParseBoxWrapperMarks4(t *testing.T) {
	for _, c := range []struct {
		src     string
		dynamic []string
	}{
		// A process of the container, maybe a shell, reads stdin.
		{`docker attach c`, []string{"stdin"}},
		{`docker container attach --sig-proxy=false c`, []string{"stdin"}},
		{`docker start -ai c`, []string{"stdin"}},
		{`docker start --interactive c`, []string{"stdin"}},
		{`podman attach --latest`, []string{"stdin"}},
		{`podman start -a -i c`, []string{"stdin"}},
		{`nerdctl attach c`, []string{"stdin"}},
		{`nerdctl start -ai c`, []string{"stdin"}},
		{`toolbox enter`, []string{"stdin"}},
		{`toolbox enter -c box`, []string{"stdin"}},
		{`toolbox --assumeyes enter box`, []string{"stdin"}},
		{`ip -b -`, []string{"stdin"}},
		{`ip -batch -`, []string{"stdin"}},
		{`ip --batch -`, []string{"stdin"}},
		{`ip -force -b /dev/stdin`, []string{"stdin"}},
		{`ip -b - <<< 'netns exec x sudo ls'`, []string{"stdin"}},
		{`distrobox ephemeral`, []string{"stdin"}},
		{`distrobox ephemeral --image alpine`, []string{"stdin"}},
		{`distrobox ephemeral --`, []string{"stdin"}},
		{`distrobox-ephemeral`, []string{"stdin"}},
		{`docker attach --no-stdin c`, nil},
		{`docker start -a c`, nil},
		{`docker start c`, nil},
		{`docker start --help -i c`, nil},
		{`toolbox enter --help`, nil},
		{`distrobox ephemeral --help`, nil},
		{`nerdctl ps`, nil},
		{`nerdctl images -a`, nil},
		{`nerdctl run IMG`, nil},
		{`ip -b`, nil},

		// The client loads code from a file of its own, which may change.
		{`docker compose ps`, []string{"source"}},
		{`docker compose -f c.yml up -d`, []string{"source"}},
		{`docker compose exec app ls`, []string{"source"}},
		{`docker-compose up`, []string{"source"}},
		{`podman compose up`, []string{"source"}},
		{`nerdctl compose up`, []string{"source"}},
		{`kubectl get pods`, []string{"source"}},
		{`kubectl -n ns describe pod x`, []string{"source"}},
		{`kubectl exec p -- ls`, []string{"source"}},
		{`oc get pods`, []string{"source"}},
		{`ip -b /tmp/f`, []string{"source"}},
		{`ip -batch f netns exec NS sudo ls`, []string{"source"}},
		{`docker compose`, nil},
		{`docker compose version`, nil},
		{`docker compose ls`, nil},
		{`docker compose --help`, nil},
		{`docker compose -v`, nil},
		{`docker-compose --version`, nil},
		{`kubectl`, nil},
		{`kubectl --help`, nil},
		{`kubectl help exec`, nil},
		{`kubectl completion bash`, nil},
		{`kubectl options`, nil},

		// Another config, which names the programs the client runs.
		{`kubectl --kubeconfig /tmp/k get pods`, []string{"rebind", "source"}},
		{`kubectl --kubeconfig=/tmp/k get pods`, []string{"rebind", "source"}},
		{`kubectl get pods --kubeconfig /tmp/k`, []string{"rebind", "source"}},
		{`kubectl --kuberc /tmp/r get pods`, []string{"rebind", "source"}},
		{`oc --kubeconfig /tmp/k exec p -- ls`, []string{"rebind", "source"}},
		{`oc --config=/tmp/k get pods`, []string{"rebind", "source"}},
		{`kubectl exec p -- kubectl --kubeconfig /tmp/k`, []string{"rebind", "source"}},
		{`kubectl exec p -- ls --kubeconfig`, []string{"source"}},
		{`KUBECONFIG=/tmp/k kubectl get pods`, []string{"rebind", "source"}},
		{`export KUBECONFIG=/tmp/k`, []string{"rebind"}},
		{`KUBERC=/tmp/r kubectl get pods`, []string{"rebind", "source"}},
		{`DOCKER_CONFIG=/tmp/d docker ps`, []string{"rebind"}},
		{`env DOCKER_CONFIG=/tmp/d nerdctl pull x`, []string{"rebind"}},
		{`docker --config /tmp/d ps`, []string{"rebind"}},
		{`docker --config=/tmp/d run IMG ls`, []string{"rebind"}},
		{`docker --config /tmp/d compose up`, []string{"rebind", "source"}},
		{`podman --config /tmp/d pull x`, []string{"rebind"}},

		// The shell of kubectl and oc with no command reads stdin.
		{`kubectl attach -i p`, []string{"source", "stdin"}},
		{`kubectl attach p`, []string{"source"}},
		{`kubectl run -it x --image=i`, []string{"source", "stdin"}},
		{`kubectl run x --image=i`, []string{"source"}},
		{`kubectl debug -it p --image=i`, []string{"source", "stdin"}},
		{`kubectl debug p --image=i`, []string{"source"}},
		{`oc rsh p`, []string{"source", "stdin"}},
		{`oc debug node/n`, []string{"source", "stdin"}},
		{`oc debug -I node/n`, []string{"source"}},
		{`oc debug p -- ls`, []string{"source"}},
		{`docker compose run app`, []string{"source", "stdin"}},
		{`docker compose run -d app`, []string{"source"}},
		{`docker compose run --entrypoint bash app`, []string{"source", "stdin"}},
		{`docker compose attach app`, []string{"source", "stdin"}},
		{`docker compose attach --no-stdin app`, []string{"source"}},

		// What they hand the command besides its words.
		{`kubectl run x --image=i --env=LD_PRELOAD=/x.so -- ls`, []string{"rebind", "source"}},
		{`kubectl debug p --image=i --env PATH=/tmp -- ls`, []string{"rebind", "source"}},
		{`oc debug p PATH=/tmp -- ls`, []string{"rebind", "source"}},
		{`oc debug p A=1 -- ls`, []string{"source"}},
		{`kubectl run x --image=i --overrides '{}' -- ls`, []string{"computed", "source"}},
		{`kubectl debug p --custom c.json -- ls`, []string{"computed", "source"}},
		{`kubectl run x --image=i ls -- -l`, []string{"computed", "source"}},
		{`kubectl run x ls --image=i`, []string{"computed", "source"}},
		{`kubectl run x --newflag --image=i -- ls`, []string{"computed", "source"}},
		{`oc rsh --shell "$s" p`, []string{"computed", "source", "stdin"}},
		{`docker compose exec -e PATH=/tmp app ls`, []string{"rebind", "source"}},
		{`docker compose run --env-from-file f app ls`, []string{"computed", "source"}},
		{`docker compose run --entrypoint "$e" app ls`, []string{"computed", "source"}},
		{`docker compose run --entrypoint 'a "b"' app`, []string{"computed", "source"}},
		{`docker compose run --entrypoint ';' app sudo ls`, []string{"computed", "source"}},
		{`docker compose run --entrypoint a --entrypoint b app`, []string{"computed", "source"}},
		{`docker compose --newflag x exec app ls`, []string{"computed", "source"}},
		{`docker compose exec --newflag app ls`, []string{"computed", "source"}},
		{`docker compose exec "$svc" ls`, []string{"computed", "source"}},
		{`nerdctl exec -e PATH=/tmp c ls`, []string{"rebind"}},
		{`nerdctl run --env-file f IMG ls`, []string{"computed"}},
		{`nerdctl --cni-path /tmp/x run IMG ls`, []string{"computed"}},
		{`nerdctl run --cni-netconfpath /tmp/x IMG ls`, []string{"computed"}},
		{`nerdctl run --cdi-spec-dirs /tmp/x IMG ls`, []string{"computed"}},
		{`nerdctl run --log-driver binary:///tmp/x IMG ls`, []string{"computed"}},
		{`nerdctl run --newflag x IMG ls`, []string{"computed"}},
		{`nerdctl run --entrypoint "$e" IMG ls`, []string{"computed"}},
		{`podman --cdi-spec-dir /tmp/x run IMG ls`, []string{"computed"}},
		{`distrobox ephemeral -r -- ls`, []string{"computed"}},
		{`distrobox ephemeral -a '--privileged' -- ls`, []string{"computed"}},
		{`distrobox ephemeral -- ls '*'`, []string{"computed"}},
		{`distrobox ephemeral -- "$c"`, []string{"computed"}},
		{`distrobox ephemeral -n "$n" -- ls`, []string{"computed"}},
		{`distrobox ephemeral --image "$i" -- ls`, []string{"computed"}},
		{`distrobox ephemeral -- ''`, []string{"computed"}},
		{`ip -batch "$f"`, []string{"computed"}},

		{`docker run --log-driver json-file IMG ls`, nil},
		{`nerdctl run --rm -v "$PWD:/x" -w /x IMG make`, nil},
		{`nerdctl run --runtime crun IMG ls`, nil},
		{`nerdctl -n "$ns" exec c ls`, nil},
		{`distrobox ephemeral --image alpine -- ls -l`, nil},
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

// The command of a container of compose and nerdctl is one of this machine,
// as that of docker; under the root of nerdctl --rootfs every path is
// another file.
func TestParseBoxWrapperPaths4(t *testing.T) {
	for _, c := range []struct {
		src     string
		writes  []string
		dynamic []string
	}{
		{`nerdctl exec c sh -c 'echo x > /etc/x'`, []string{"/etc/x"}, nil},
		{`docker compose exec app sh -c 'echo x > out'`, []string{"/w/out"}, []string{"source"}},
		{`nerdctl run --rootfs /srv rm /etc/x`, nil, []string{"computed"}},
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
	}
}

// With deny = ["sudo *"] the sudo behind these wrappers is denied, and so it
// is by Cedar; what hands stdin to a shell, loads code from a file or
// another config asks, and the rest passes.
func TestBoxWrappersPolicy4(t *testing.T) {
	ctx := context.Background()
	rules, err := Load(ctx, t.TempDir(), Rules{Deny: []string{"sudo *"}})
	if err != nil {
		t.Fatal(err)
	}
	cedar := mustLoad(t, map[string]string{"a.cedar": permitAll + `@reason("not this")
forbid(principal, action == Action::"run", resource == Command::"sudo");
`})
	for _, cmd := range boxSudo4 {
		for _, e := range []*Engine{rules, cedar} {
			if d := check(t, e, bash(cmd)); d.Action != Deny {
				t.Errorf("%s: %+v, want deny", cmd, d)
			}
		}
	}
	for _, c := range []struct{ cmd, want string }{
		{`nerdctl ps`, Allow},
		{`nerdctl exec c ls`, Allow},
		{`docker compose version`, Allow},
		{`docker attach c`, Ask},
		{`docker start -ai c`, Ask},
		{`toolbox enter`, Ask},
		{`ip -b -`, Ask},
		{`ip -b /tmp/f`, Ask},
		{`docker compose ps`, Ask},
		{`kubectl get pods`, Ask},
		{`kubectl --kubeconfig /tmp/k get pods`, Ask},
		{`KUBECONFIG=/tmp/k kubectl get pods`, Ask},
		{`docker --config /tmp/d ps`, Ask},
	} {
		if d := check(t, rules, bash(c.cmd)); d.Action != c.want {
			t.Errorf("%s: %+v, want %s", c.cmd, d, c.want)
		}
	}
}
