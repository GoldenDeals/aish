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
when { context.flags.containsAny(["r", "R"]) && context.paths.containsAny(["/", context.home]) };

@reason("force push is not allowed")
forbid(principal, action == Action::"run", resource == Command::"git")
when { context.args.contains("push") && context.flags.containsAny(["f", "force", "force-with-lease"]) };

@reason("writing outside $HOME")
forbid(principal, action == Action::"write", resource)
unless { resource in Dir::"~" || resource in Dir::"/tmp" };

@ask("could not parse the command")
forbid(principal, action == Action::"run", resource)
when { context has parse_error };

@ask("installs packages")
forbid(principal, action == Action::"run", resource)
when {
  resource in [Command::"apt", Command::"apt-get", Command::"pacman", Command::"dnf", Command::"brew", Command::"yay"] &&
  context.args.containsAny(["install", "-S", "-Syu", "upgrade", "remove", "-R"])
};
