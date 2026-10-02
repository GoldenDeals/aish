// Example aish policy. Copy to ~/.config/aish/policy/.
// Cedar is default-deny: nothing is allowed without a permit. This permits
// everything, then forbids the dangerous part; @ask asks instead of denying,
// @reason is the text the model and the user see.

permit(principal, action, resource);

@reason("sudo is not allowed for the agent")
forbid(principal, action == Action::"run", resource == Command::"sudo");

@reason("the agent must not end or replace the shell")
forbid(principal, action == Action::"run", resource)
when { resource in [Command::"exit", Command::"exec", Command::"logout"] };

@reason("recursive delete of / or $HOME")
forbid(principal, action == Action::"run", resource == Command::"rm")
when { context.flags.containsAny(["r", "R", "recursive"]) && context.paths.containsAny(["/", context.home]) };

// A refspec starting with + (git push origin +main) forces the push without
// a flag. A set has no prefix match, so the operand is found in the text.
@reason("force push is not allowed")
forbid(principal, action == Action::"run", resource == Command::"git")
when {
  context.args.contains("push") &&
  (context.flags.containsAny(["f", "force", "force-with-lease"]) || context.text like "* +*")
};

@reason("writing outside $HOME")
forbid(principal, action == Action::"write", resource)
unless { resource in Dir::"~" || resource in Dir::"/tmp" };

@ask("could not parse the command")
forbid(principal, action == Action::"run", resource)
when { context has parse_error };

// eval "$x", $cmd, echo … | bash, source, PROMPT_COMMAND=…: code no rule
// above has seen. To let source through, add
// unless { context has dynamic && context.dynamic == ["source"] }
@ask("the command is built at run time")
forbid(principal, action == Action::"run", resource)
when { context has dynamic };

@ask("installs packages")
forbid(principal, action == Action::"run", resource)
when {
  resource in [Command::"apt", Command::"apt-get", Command::"pacman", Command::"dnf", Command::"brew", Command::"yay"] &&
  context.args.containsAny(["install", "-S", "-Syu", "upgrade", "remove", "-R"])
};
