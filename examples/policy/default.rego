# Example aish policy. Copy to ~/.config/aish/policy/.
#
# input: {tool, args, cwd, home, path, commands, parse_error}
#   commands: argv of every simple command in a bash call, including pipes,
#             $(...), subshells and `bash -c '...'` strings.
# decision: {"action": "allow"|"deny"|"ask", "reason": "..."}; undefined = allow.
package aish

import rego.v1

default decision := {"action": "allow"}

decision := {"action": "deny", "reason": concat("; ", deny)} if {
	count(deny) > 0
} else := {"action": "ask", "reason": concat("; ", ask)} if {
	count(ask) > 0
}

# Programs started by the call. For `sudo rm x` commands holds both
# ["sudo", "rm", "x"] and ["rm", "x"].
programs contains base(argv[0]) if some argv in input.commands

base(p) := regex.replace(p, `^.*/`, "")

deny contains "sudo is not allowed for the agent" if "sudo" in programs

deny contains "the agent must not end or replace the shell" if {
	some argv in input.commands
	argv[0] in {"exit", "exec", "logout"}
}

deny contains "recursive delete of / or $HOME" if {
	some argv in input.commands
	base(argv[0]) == "rm"
	some flag in argv
	regex.match(`^-[a-zA-Z]*[rR]`, flag)
	some target in argv
	target in {"/", "/*", "~", "~/", "$HOME", "${HOME}", input.home, concat("", [input.home, "/"])}
}

deny contains "force push is not allowed" if {
	some argv in input.commands
	base(argv[0]) == "git"
	"push" in argv
	some flag in argv
	flag in {"-f", "--force", "--force-with-lease"}
}

deny contains sprintf("writing outside $HOME: %s", [input.path]) if {
	input.tool in {"write_file", "edit_file"}
	not startswith(input.path, concat("", [input.home, "/"]))
	not startswith(input.path, "/tmp/")
}

ask contains "could not parse the command" if input.parse_error

ask contains "installs packages" if {
	some argv in input.commands
	base(argv[0]) in {"apt", "apt-get", "pacman", "dnf", "brew", "yay"}
	some op in argv
	op in {"install", "-S", "-Syu", "upgrade", "remove", "-R"}
}
