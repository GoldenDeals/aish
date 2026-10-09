package main

import (
	"strings"
	"testing"

	"github.com/GoldenDeals/aish/internal/config"
	"github.com/GoldenDeals/aish/internal/mcp"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// aish status says, in red, that yolo has the checks off, and says nothing
// of it while they are on.
func TestStatusYolo(t *testing.T) {
	disk := diskConfig(t, "")
	for _, yolo := range []bool{true, false} {
		fakeProxy(t, map[string]any{
			rpc.MethodStatus:  rpc.Status{Info: rpc.Info{Model: config.Default().Model, Yolo: yolo}},
			rpc.MethodConfig:  rpc.Config{Config: config.Default()},
			rpc.MethodMCPList: mcp.ListResult{},
		})
		code, out, stderr := captured(t, func() int { return statusCmd(disk) })
		if code != 0 || stderr != "" {
			t.Fatalf("yolo %v: exit %d, stderr %q", yolo, code, stderr)
		}
		const line = "\x1b[31mon: no policies, [policy] rules or questions for the assistant till this shell exits; aish yolo off\x1b[0m"
		plain := ansi.ReplaceAllString(out, "")
		if got := strings.Contains(out, line) && strings.Contains(plain, "  yolo             on: "); got != yolo {
			t.Errorf("yolo %v: the line is there %v:\n%s", yolo, got, out)
		}
		if !yolo && strings.Contains(plain, "\n  yolo ") {
			t.Errorf("yolo off, said:\n%s", plain)
		}
	}
}
