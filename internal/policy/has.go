package policy

import (
	"fmt"

	"github.com/cedar-policy/cedar-go/types"
	xast "github.com/cedar-policy/cedar-go/x/exp/ast"
	"github.com/cedar-policy/cedar-go/x/exp/schema/resolved"
)

// contextAttrs is every context attribute of every action in the schema.
// The union rather than the attributes of the policy's own action: it is
// enough to catch a typo and never flags a policy without `action ==`.
func contextAttrs(s *resolved.Schema) map[types.String]bool {
	attrs := map[types.String]bool{}
	for _, a := range s.Actions {
		if a.AppliesTo == nil {
			continue
		}
		for name := range a.AppliesTo.Context {
			attrs[name] = true
		}
	}
	return attrs
}

// checkHas rejects `context has <name>` for a name the schema does not
// know. The validator types such a test as always false and lets the
// policy through, so a misspelled has would be a rule that never fires.
func checkHas(p *xast.Policy, attrs map[types.String]bool) error {
	var err error
	for _, c := range p.Conditions {
		xast.Inspect(xast.NewNode(c.Body), func(n xast.IsNode) bool {
			if err != nil {
				return false
			}
			has, ok := n.(xast.NodeTypeHas)
			if !ok {
				return true
			}
			if v, ok := has.Arg.(xast.NodeTypeVariable); ok && v.Name == "context" && !attrs[has.Value] {
				err = fmt.Errorf("context has no attribute %q (the schema: README, «Политики»)", string(has.Value))
				return false
			}
			return true
		})
		if err != nil {
			return err
		}
	}
	return nil
}
