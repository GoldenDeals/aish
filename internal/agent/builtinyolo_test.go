package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/policy"
	"github.com/GoldenDeals/aish/internal/tools"
)

// aish yolo lifts the built-in policy with the other Cedar policies: its
// questions are not asked and its denials do not stand; the guard does.
func TestYoloLiftsBuiltinPolicy(t *testing.T) {
	pol, err := policy.Load(context.Background(), "", policy.Rules{Builtin: true})
	if err != nil {
		t.Fatal(err)
	}
	no := func(a *Agent) { a.UI.(*fakeUI).answer = "n" }
	for _, command := range []string{"sudo ls", "git reset --hard HEAD~3"} {
		args := argsJSON(t, map[string]string{"command": command})
		handed, result, asked := yoloCall(t, pol, true, tools.Bash, args, no)
		if handed != command || result != "" || len(asked) != 0 {
			t.Errorf("%s under yolo: handed %q, result %q, asked %q", command, handed, result, asked)
		}
		handed, _, asked = yoloCall(t, pol, false, tools.Bash, args, no)
		if handed != "" || len(asked) != 1 {
			t.Errorf("%s with yolo off: handed %q, asked %q", command, handed, asked)
		}
	}
	args := argsJSON(t, map[string]string{"command": "mkfs.ext4 /dev/sdb1"})
	if handed, _, asked := yoloCall(t, pol, true, tools.Bash, args, nil); handed != "mkfs.ext4 /dev/sdb1" || len(asked) != 0 {
		t.Errorf("mkfs under yolo: handed %q, asked %q", handed, asked)
	}
	if handed, result, _ := yoloCall(t, pol, false, tools.Bash, args, nil); handed != "" ||
		!strings.HasPrefix(result, "denied by policy: formats or overwrites a disk") {
		t.Errorf("mkfs with yolo off: handed %q, result %q", handed, result)
	}
	args = argsJSON(t, map[string]string{"command": "trap 'aish yolo' DEBUG"})
	if handed, result, _ := yoloCall(t, pol, true, tools.Bash, args, nil); handed != "" || result != "denied by policy: "+policy.YoloReason {
		t.Errorf("aish yolo under yolo: handed %q, result %q", handed, result)
	}
}
