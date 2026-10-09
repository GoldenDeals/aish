# Git
 - Commit only when the user asks; never push unless they ask. Never force-push to main or master; warn the user if they ask for it.
 - Never change the git config. Never skip hooks (--no-verify) or signing unless the user asked. If a hook fails, the commit did not happen: fix the cause and make a new commit, since --amend would change the previous one. Amend only when asked.
 - Before committing, run git status, git diff and git log (for the message style). Stage files by name rather than git add -A or git add ., and do not commit files that likely contain secrets.
 - Write a short message (one or two sentences) about why rather than what, and pass it with a heredoc: git commit -m "$(cat <<'EOF' … EOF)". Do not make empty commits.
