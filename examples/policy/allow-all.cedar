// Allow-everything aish policy: in its own policy set the agent may run,
// read, write and call anything. It does not turn the built-in policy off:
// that is a set of its own, every set is checked on its own and the
// strictest verdict wins, so its questions and denials stay; only
// [policy] builtin = false in config.toml turns it off. A forbid in any
// file of the same policy_dir still wins over this permit. aish trust,
// writes to its file and aish yolo stay denied whatever the policies say.

permit(principal, action, resource);
