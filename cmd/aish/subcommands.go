package main

// UserCommands are the subcommands meant for the user at the prompt, so
// the proxy makes each a command of its own: `status` for `aish status`.
// Not `agent` and `init`, which are plumbing, nor `tool`, too common a
// name. Kept next to the switch in run so a new subcommand is not
// forgotten here.
var UserCommands = []string{"agents", "clear", "compact", "expand", "hooks", "mcp", "model", "new", "policy", "resume", "session", "skills", "status"}
