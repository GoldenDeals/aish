package policy

// dbxUse is where the programs of distrobox put the value of a variable of
// dbxVars.
type dbxUse int

const (
	// dbxQuoted is a value in double quotes in the line distrobox-create
	// evals.
	dbxQuoted dbxUse = iota
	// dbxBare is a word of that line, unquoted.
	dbxBare
	// dbxProgram is a program run with its words, and the first words of
	// that line.
	dbxProgram
)

// dbxVars are the variables of distrobox 1.8 whose values its programs run
// as code. Each of them runs the container manager of DBX_CONTAINER_MANAGER,
// and, with --root, the program of DBX_SUDO_PROGRAM before it;
// distrobox-create takes the others in place of its options, and puts the
// image bare and the name, the hostname and the homes in double quotes in
// the line of the manager it evals (see createCode). distrobox ephemeral,
// enter and assemble run distrobox-create with them in its environment, so
// they are commandVars: a static value is parsed (see dbxCode) wherever the
// line assigns it, whatever it runs; one made at run time is computed. The
// others of DBX_ are numbers or switches.
var dbxVars = map[string]dbxUse{
	"DBX_CONTAINER_CUSTOM_HOME": dbxQuoted,
	"DBX_CONTAINER_HOME_PREFIX": dbxQuoted,
	"DBX_CONTAINER_HOSTNAME":    dbxQuoted,
	"DBX_CONTAINER_IMAGE":       dbxBare,
	"DBX_CONTAINER_MANAGER":     dbxProgram,
	"DBX_CONTAINER_NAME":        dbxQuoted,
	"DBX_SUDO_PROGRAM":          dbxProgram,
}

func init() {
	for name := range dbxVars {
		commandVars[name] = true
	}
}

// dbxCode is the code of value, static, of the variable name of dbxVars, as
// it is in the line distrobox-create evals; autodetect, the manager by
// default, is none.
func dbxCode(name, value string) ([]string, bool) {
	use, ok := dbxVars[name]
	switch {
	case !ok:
		return nil, false
	case name == "DBX_CONTAINER_MANAGER" && value == "autodetect":
		return nil, true
	case use == dbxQuoted:
		return []string{`: "` + value + `"`}, true
	case use == dbxBare:
		return []string{": " + value}, true
	}
	return []string{value}, true
}
