package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/GoldenDeals/aish/internal/rpc"
)

// applyConfigCmd is `aish apply-config`: the proxy reads the config files
// anew and puts them in force for this shell. It reads them as it starts
// and with this command only, so that an edit is in force when the user
// says so, all of it at once.
func applyConfigCmd(args []string) int {
	if len(args) > 0 {
		return fail(errors.New("usage: aish apply-config"))
	}
	client, err := rpc.FromEnv()
	if err != nil {
		return fail(err)
	}
	var res rpc.Applied
	if err := client.Call(rpc.MethodApplyConfig, shellParams(), &res); err != nil {
		return fail(err)
	}
	fmt.Print(appliedText(res))
	return 0
}

// appliedText is what aish apply-config prints of res: what changed, the
// profile, the model and the effort of the shell if they followed, and what
// waits for a restart.
func appliedText(res rpc.Applied) string {
	var b strings.Builder
	if len(res.Keys) > 0 {
		fmt.Fprintf(&b, "applied config.toml: %s\n", strings.Join(res.Keys, ", "))
	}
	for _, f := range res.Files {
		fmt.Fprintf(&b, "applied %s\n", home(f))
	}
	if b.Len() == 0 {
		b.WriteString("applied; nothing changed since the config was read\n")
	}
	if res.Switched {
		if res.Info.Profile != "" {
			fmt.Fprintf(&b, "profile %s, ", res.Info.Profile)
		}
		fmt.Fprintf(&b, "model %s, effort %s for this shell\n", res.Info.Model, effortName(res.Info.Effort))
	}
	if len(res.Restart) > 0 {
		names := make([]string, len(res.Restart))
		for i, k := range res.Restart {
			names[i] = home(k)
		}
		fmt.Fprintf(&b, "\x1b[33mrestart aish to apply %s\x1b[0m\n", strings.Join(names, ", "))
	}
	return b.String()
}
