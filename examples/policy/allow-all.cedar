// Allow-everything aish policy: the agent may run, read, write and call
// anything. Use it alone in a policy_dir, instead of default.cedar: a forbid
// in any file of the same directory still wins over this permit, and every
// policy_dir is checked on its own, the strictest verdict winning. aish trust
// and writes to its file stay denied whatever the policies say.

permit(principal, action, resource);
