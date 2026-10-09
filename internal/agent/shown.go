package agent

import (
	"strings"

	"github.com/GoldenDeals/aish/internal/capture"
)

// shownReason is the reason of a policy or a hook as the terminal shows it,
// in the question about a call or after a call denied: the hook may have
// it from the model's call. Its lines break at \r\n, \n or \r, as the
// question draws them (askLines in the proxy); the other control
// characters show as signs (capture.Visible). The model is told the reason
// as it was given.
func shownReason(s string) string {
	return capture.Visible(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"))
}
