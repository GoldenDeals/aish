// Built-in aish policy: the safety net in force unless [policy] builtin =
// false in config.toml. It catches the mistakes that cost the most and asks
// about little else: work inside the project runs without a question.
// Policies here judge what the line names, not what a program does: an
// interpreter, a script or a variable may still do anything.
//
// It is a policy set of its own, next to those of policy_dir: a permit of
// theirs does not lift a forbid of this one. `aish policy --builtin` prints
// this text, to copy into policy_dir, change there and turn this one off.

permit(principal, action, resource);

// ===== deny: never right for the agent =====

@reason("the agent must not end or replace the shell")
forbid(principal, action == Action::"run", resource)
when { resource in [Command::"exit", Command::"exec", Command::"logout"] };

@reason("wipes /, a top-level directory or $HOME")
@hint("Never delete or chmod/chown -R /, a top-level directory such as /usr, or the home directory itself.")
forbid(principal, action == Action::"run", resource)
when {
  (resource in [Command::"rm", Command::"rmdir", Command::"shred"] ||
   (resource in [Command::"chmod", Command::"chown", Command::"chgrp"] && context.flags.containsAny(["R", "recursive"]))) &&
  (context.paths.containsAny(["/", context.home]) ||
   context.paths.containsAny(["/bin", "/boot", "/dev", "/etc", "/home", "/lib", "/lib64", "/mnt", "/media", "/opt",
                              "/proc", "/root", "/run", "/sbin", "/srv", "/sys", "/tmp", "/usr", "/var"]))
};

@reason("formats or overwrites a disk")
forbid(principal, action == Action::"run", resource)
when {
  context.program like "mkfs*" ||
  resource in [Command::"wipefs", Command::"blkdiscard", Command::"sfdisk", Command::"sgdisk", Command::"mkswap"] ||
  (resource == Command::"dd" && context.text like "*of=/dev/*")
};

// ===== ask =====

// sudo as a word of argv as well: rlwrap sudo, chronic sudo and other
// launchers the parser does not unwrap.
@ask("runs as root")
@hint("sudo, doas, su, pkexec and run0 need the user's yes: prefer what runs without root, or give the user the command.")
forbid(principal, action == Action::"run", resource)
when {
  resource in [Command::"sudo", Command::"sudoedit", Command::"doas", Command::"su", Command::"pkexec", Command::"run0"] ||
  (context.args.containsAny(["sudo", "doas", "pkexec", "run0", "/usr/bin/sudo", "/bin/sudo"]) &&
   !(resource in [Command::"grep", Command::"rg", Command::"man", Command::"which", Command::"type", Command::"apt",
                  Command::"apt-get", Command::"pacman", Command::"dnf", Command::"brew"]))
};

// $d, $(…), `…` or {} among the words, or none at all (xargs adds them):
// the target is known only at run time, and an empty variable makes
// rm -rf "$dir/" rm -rf /. A bare * takes all a directory holds.
@ask("deletes a path known only at run time, or all of a directory")
@hint("Write the paths of rm -r literally, without $VAR, $(...), xargs or a bare *: such a delete needs the user's yes.")
forbid(principal, action == Action::"run", resource)
when {
  resource in [Command::"rm", Command::"rmdir", Command::"shred"] &&
  context.flags.containsAny(["r", "R", "recursive"]) &&
  (context.text like "*$*" || context.text like "*`*" || context.text like "*{}*" || context.operands.isEmpty() ||
   context.operands.containsAny(["*", "./*", ".*", "~/*", "/*", "$HOME/*", "${HOME}/*"]))
};

// find -delete deletes what its tests match, not its root: a question.
@ask("deletes files found across /, a top-level directory or $HOME")
forbid(principal, action == Action::"run", resource == Command::"find")
when {
  context.args.contains("-delete") &&
  (context.paths.containsAny(["/", context.home]) ||
   context.paths.containsAny(["/bin", "/boot", "/dev", "/etc", "/home", "/lib", "/lib64", "/mnt", "/media", "/opt",
                              "/proc", "/root", "/run", "/sbin", "/srv", "/sys", "/tmp", "/usr", "/var"]))
};

// A refspec starting with + (git push origin +main) forces the push without
// a flag, one starting with : deletes the remote branch. A set has no
// prefix match, so the operand is found in the text.
@ask("discards uncommitted work, a branch or remote history")
@hint("git reset --hard, clean -f, checkout -- ., restore, stash drop/clear, branch -D and force push need the user's yes.")
forbid(principal, action == Action::"run", resource == Command::"git")
when {
  (context.args.contains("push") &&
   (context.flags.containsAny(["f", "force", "force-with-lease", "force-if-includes", "mirror", "delete", "d"]) ||
    context.text like "* +*" || context.text like "* :*")) ||
  (context.args.contains("reset") && context.flags.containsAny(["hard", "merge", "keep"])) ||
  (context.args.contains("clean") && context.flags.containsAny(["f", "force"])) ||
  (context.args.contains("checkout") && (context.args.containsAny(["--", "."]) || context.flags.containsAny(["f", "force"]))) ||
  (context.args.contains("restore") && !context.flags.containsAny(["staged", "S"])) ||
  (context.args.contains("stash") && context.args.containsAny(["drop", "clear"])) ||
  (context.args.contains("branch") &&
   (context.flags.contains("D") || context.flags.containsAny(["d", "delete"]) && context.flags.containsAny(["f", "force"])))
};

@ask("writes outside $HOME and /tmp")
forbid(principal, action == Action::"write", resource)
unless { resource in Dir::"~" || resource in Dir::"/tmp" || resource in Dir::"/var/tmp" };

// Code put here runs later, in the user's shell or programs, past every
// check of the agent's calls. Only what a redirection or a file tool
// writes is seen: sed -i, tee, cp and an interpreter are not.
@ask("changes what runs later: startup files, keys, hooks or aish's own settings")
@hint("Do not edit shell startup files, ~/.ssh, git hooks or config, autostart entries or aish's settings unless the user asked for that file: such a write needs the user's yes.")
forbid(principal, action == Action::"write", resource)
when {
  context.path like "*/.bashrc" || context.path like "*/.bash_profile" || context.path like "*/.bash_login" ||
  context.path like "*/.bash_logout" || context.path like "*/.profile" || context.path like "*/.inputrc" ||
  context.path like "*/.bashrc.d/*" || context.path like "*/.zshrc" || context.path like "*/.zshenv" ||
  context.path like "*/.zprofile" || context.path like "*/.zlogin" || context.path like "*/.zlogout" ||
  context.path like "*/.ssh/*" || context.path like "*/.gnupg/*" || context.path like "*/.envrc" ||
  context.path like "*/.gitconfig" || context.path like "*/.config/git/*" || context.path like "*/.git/config" ||
  context.path like "*/.git/hooks/*" || context.path like "*/.config/autostart/*" ||
  context.path like "*/.config/systemd/user/*" || context.path like "*/aish/config.toml" ||
  context.path like "*/aish/policy/*" || context.path like "*/aish/hooks/*" || context.path like "*/aish/tools/*" ||
  context.path like "*/aish/skills/*" || context.path like "*/aish/agents/*" || context.path like "*/.aish.toml" ||
  context.path like "*/.claude/skills/*" || context.path like "*/.claude/agents/*"
};

@ask("could not parse the command")
forbid(principal, action == Action::"run", resource)
when { context has parse_error };

// source runs the code of a file no rule has seen, as ./script does, which
// runs without a question. Code built at run time or read from a pipe, a
// new PATH or a git setting with code, and a hook on the user's prompt
// are asked about: the last two stay in the user's shell after the request.
@ask("runs code built at run time, or changes what names run later")
@hint("Write commands with literal program names and paths: $cmd, eval \"$x\", curl ... | sh, export PATH=..., git config with commands and PROMPT_COMMAND=... need the user's yes.")
forbid(principal, action == Action::"run", resource)
when { context has dynamic && context.dynamic != ["source"] };
