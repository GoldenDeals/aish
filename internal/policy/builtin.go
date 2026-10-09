package policy

import _ "embed"

// BuiltinText is the built-in policy, builtin.cedar: a Cedar policy set of
// its own, in force next to the guard unless [policy] builtin = false. It
// comes with the binary, so that its rules follow the parser's marks
// (context.dynamic, the paths) as they grow; `aish policy --builtin`
// prints it for a user who would rather keep a changed copy in policy_dir.
//
//go:embed builtin.cedar
var BuiltinText string

// builtinName is the file the built-in policy is named by in its policy
// IDs, positions and errors.
const builtinName = "builtin.cedar"

// loadBuiltin is the checker of the built-in policy. It is validated as a
// file of policy_dir is: a rule naming an attribute the schema lost would
// fail every Load rather than never fire.
func loadBuiltin() (*cedarChecker, error) {
	c, err := compileCedar([]string{builtinName}, func(string) ([]byte, error) { return []byte(BuiltinText), nil })
	if err != nil {
		return nil, err
	}
	for i := range c.summary {
		c.summary[i].Builtin = true
	}
	return c, nil
}
