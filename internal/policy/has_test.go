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
		`forbid(principal, action, resource) when { context has path };` + "\n"})
}

// The entities of the schema have no attributes, only File and Tool have
// tags, so `resource has path` is false whatever the call, and the
// validator lets it through the same way as a misspelled context has.
func TestHasOnEntityIsLoadError(t *testing.T) {
	for _, tc := range []struct{ cond, name string }{
		{`resource has path`, "path"},
		{`principal has x`, "x"},
		{`context.tool == "read" && resource has path`, "path"},
		{`!(principal has name)`, "name"},
	} {
		src := permitAll + `forbid(principal, action, resource) when { ` + tc.cond + ` };` + "\n"
		_, err := load(t, map[string]string{"ent.cedar": src})
		if err == nil {
			t.Errorf("%s: loaded", tc.cond)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, `"`+tc.name+`"`) || !strings.Contains(msg, "hasTag") || !strings.Contains(msg, "ent.cedar:2") {
			t.Errorf("%s: error does not name the attribute, hasTag and the file:line: %v", tc.cond, err)
		}
	}
}

func TestHasTagOnEntityLoads(t *testing.T) {
	mustLoad(t, map[string]string{"ok.cedar": permitAll +
		`forbid(principal, action, resource) when { resource.hasTag("x") };` + "\n" +
		`forbid(principal, action == Action::"read", resource) when { resource.hasTag("x") && resource.getTag("x") == "y" };` + "\n"})
}
