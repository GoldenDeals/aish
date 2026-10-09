// Stricter aish policy, on top of the built-in one: questions about what
// leaves the machine or the project. Copy to ~/.config/aish/policy/; the
// built-in policy stays in force next to it (aish policy --builtin prints
// it). Cedar is default-deny, so this permits everything and then asks;
// @ask is the question, @hint the text the model gets in advance.

permit(principal, action, resource);

@ask("publishes commits to a remote")
@hint("git push needs the user's yes: push only when the user asked for it.")
forbid(principal, action == Action::"run", resource == Command::"git")
when { context.args.contains("push") };

@ask("runs commands or copies files on another machine")
forbid(principal, action == Action::"run", resource)
when {
  resource in [Command::"ssh", Command::"scp", Command::"sftp", Command::"mosh"] ||
  (resource == Command::"rsync" && context.text like "*:*")
};

@ask("sends data or files over the network")
forbid(principal, action == Action::"run", resource)
when {
  resource in [Command::"nc", Command::"ncat", Command::"netcat", Command::"socat", Command::"telnet"] ||
  (resource in [Command::"curl", Command::"wget"] &&
   (context.flags.containsAny(["d", "data", "data-binary", "data-raw", "data-urlencode", "data-ascii", "F", "form",
                               "form-string", "T", "upload-file", "json", "post-data", "post-file", "body-data",
                               "body-file"]) ||
    (context.flags.containsAny(["X", "request", "method"]) &&
     context.args.containsAny(["POST", "PUT", "PATCH", "DELETE", "post", "put", "patch", "delete"]))))
};

@ask("posts to GitHub or GitLab")
forbid(principal, action == Action::"run", resource)
when {
  resource in [Command::"gh", Command::"glab"] &&
  (context.args.containsAny(["create", "merge", "close", "reopen", "comment", "review", "edit", "delete", "fork",
                             "archive", "transfer", "rename", "upload", "lock"]) ||
   (context.args.contains("api") && context.flags.containsAny(["X", "method", "f", "F", "field", "raw-field", "input"])))
};

@ask("installs or removes system packages")
forbid(principal, action == Action::"run", resource)
when {
  (resource in [Command::"apt", Command::"apt-get", Command::"aptitude", Command::"dnf", Command::"yum", Command::"zypper",
                Command::"apk", Command::"brew", Command::"port", Command::"snap", Command::"flatpak", Command::"nix-env"] &&
   context.args.containsAny(["install", "reinstall", "remove", "purge", "autoremove", "upgrade", "dist-upgrade",
                             "full-upgrade", "uninstall", "erase", "add", "del", "-i", "-e", "-u"])) ||
  (resource in [Command::"pacman", Command::"yay", Command::"paru"] &&
   context.flags.containsAny(["S", "R", "U", "sync", "remove", "upgrade"]))
};

@ask("installs a program for the whole user, outside the project")
forbid(principal, action == Action::"run", resource)
when {
  (resource in [Command::"npm", Command::"pnpm", Command::"yarn", Command::"bun"] && context.flags.containsAny(["g", "global"])) ||
  (resource in [Command::"pip", Command::"pip3"] && context.args.contains("install") &&
   context.flags.containsAny(["user", "break-system-packages"])) ||
  (resource in [Command::"cargo", Command::"go", Command::"gem", Command::"pipx"] && context.args.contains("install"))
};

@ask("changes system services, power state or scheduled jobs")
forbid(principal, action == Action::"run", resource)
when {
  resource in [Command::"shutdown", Command::"reboot", Command::"poweroff", Command::"halt", Command::"at", Command::"batch"] ||
  (resource == Command::"crontab" && !context.flags.contains("l")) ||
  (resource == Command::"systemctl" && !context.flags.contains("user") &&
   context.args.containsAny(["start", "stop", "restart", "reload", "enable", "disable", "mask", "kill", "isolate",
                             "reboot", "poweroff", "suspend", "hibernate", "daemon-reload"]))
};

@ask("reads a private key or credentials")
forbid(principal, action == Action::"read", resource)
when {
  context.path like "*/.ssh/id_*" || context.path like "*/.gnupg/*" || context.path like "*/.aws/credentials" ||
  context.path like "*/.netrc" || context.path like "*/.docker/config.json" || context.path like "*/.password-store/*"
};

@ask("reads a private key or credentials")
forbid(principal, action == Action::"run", resource)
when {
  !(resource in [Command::"chmod", Command::"ls", Command::"stat", Command::"ssh-keygen", Command::"ssh-add"]) &&
  (context.text like "*.ssh/id_*" || context.text like "*.aws/credentials*" || context.text like "*.netrc*" ||
   context.text like "*.password-store*" ||
   (resource == Command::"gpg" && context.flags.containsAny(["export-secret-keys", "export-secret-subkeys"])))
};

@ask("writes outside the working directory")
forbid(principal, action == Action::"write", resource)
unless { resource in Dir::"." || resource in Dir::"/tmp" || resource in Dir::"/var/tmp" };
