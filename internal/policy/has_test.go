package policy

import (
	"strings"
	"testing"
)

// The validator types `context has <unknown>` as always false instead of
// rejecting it, so a misspelled has would load as a rule that never fires.
func TestHasTypoIsLoadError(t *testing.T) {
	for _, cond := range []string{
		`context has comands`,
		`context.program == "rm" && context has comands`,
		`context.program == "rm" || context has comands`,
		`if context has comands then true else false`,
		`!(context has comands)`,
	} {
		src := permitAll + "\n" + `forbid(principal, action == Action::"run", resource) when { ` + cond + ` };` + "\n"
		_, err := load(t, map[string]string{"typo.cedar": src})
		if err == nil {
			t.Errorf("%s: loaded", cond)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "comands") || !strings.Contains(msg, "typo.cedar:3") {
			t.Errorf("%s: error does not name the attribute and the file:line: %v", cond, err)
		}
	}
}

func TestHasUnlessTypoIsLoadError(t *testing.T) {
	_, err := load(t, map[string]string{"typo.cedar": permitAll +
		`forbid(principal, action, resource) unless { context has severs };` + "\n"})
	if err == nil || !strings.Contains(err.Error(), "severs") {
		t.Errorf("a has typo in unless: %v", err)
	}
}

func TestHasKnownAttributeLoads(t *testing.T) {
	mustLoad(t, map[string]string{"ok.cedar": permitAll +
		`forbid(principal, action == Action::"run", resource) when { context has parse_error };` + "\n" +
		`forbid(principal, action == Action::"call", resource) when { context has server && context.server == "x" };` + "\n" +
		// The union over all actions: path is only in read/write/call, and a
		// policy without action == may test it.
		`forbid(principal, action, resource) when { context has path };` + "\n" +
		// has on an entity is a different question, out of this check.
		`forbid(principal, action, resource) when { resource has anything };` + "\n"})
}
