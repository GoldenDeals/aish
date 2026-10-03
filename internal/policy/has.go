package policy

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cedar-policy/cedar-go/types"
	xast "github.com/cedar-policy/cedar-go/x/exp/ast"
	"github.com/cedar-policy/cedar-go/x/exp/schema/resolved"
)

// hasNames is what `has` may test on the variables of a policy: the
// context attributes of every action and the attributes of every principal
// and resource entity type. The union rather than the attributes of the
// policy's own action: it is enough to catch a typo and never flags a
// policy without `action ==`.
type hasNames struct {
	attrs map[types.String]map[types.String]bool
	// tagged names the entity types with tags, for the hint: their data is
	// in tags, which has does not see.
	tagged []string
}

func newHasNames(s *resolved.Schema) hasNames {
	h := hasNames{attrs: map[types.String]map[types.String]bool{
		"context": {}, "principal": {}, "resource": {},
	}}
	shape := func(v types.String, ts []types.EntityType) {
		for _, t := range ts {
			for name := range s.Entities[t].Shape {
				h.attrs[v][name] = true
			}
		}
	}
	for _, a := range s.Actions {
		if a.AppliesTo == nil {
			continue
		}
		for name := range a.AppliesTo.Context {
			h.attrs["context"][name] = true
		}
		shape("principal", a.AppliesTo.Principals)
		shape("resource", a.AppliesTo.Resources)
	}
	for t, e := range s.Entities {
		if e.Tags != nil {
			h.tagged = append(h.tagged, string(t))
		}
	}
	sort.Strings(h.tagged)
	return h
}

// checkHas rejects `context has <name>`, `principal has <name>` and
// `resource has <name>` for a name the schema does not know. The validator
// types such a test as always false and lets the policy through, so a
// misspelled has would be a rule that never fires.
func checkHas(p *xast.Policy, h hasNames) error {
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
			v, ok := has.Arg.(xast.NodeTypeVariable)
			if !ok {
				return true
			}
			if attrs, ok := h.attrs[v.Name]; ok && !attrs[has.Value] {
				err = h.missing(v.Name, has.Value)
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

func (h hasNames) missing(v, name types.String) error {
	if v == "context" || len(h.attrs[v]) > 0 {
		return fmt.Errorf("%s has no attribute %q (the schema: README, «Политики»)", v, string(name))
	}
	if len(h.tagged) == 0 {
		return fmt.Errorf("%s has no attribute %q: the entities have no attributes", v, string(name))
	}
	return fmt.Errorf("%s has no attribute %q: the entities have no attributes, only tags (hasTag/getTag) on %s",
		v, string(name), strings.Join(h.tagged, ", "))
}
