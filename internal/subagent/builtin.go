package subagent

// builtins are the subagents aish has without a file, named as Claude
// Code's are: prompts and skills written for it delegate to
// general-purpose and Explore. Find takes them as the farthest root, so a
// file of the same name, in any agents directory, replaces one. Their names
// are in __aish_is_agent and __aish_comp_agent of init.bash and init.zsh
// too: a line "&Explore text" has no file to find.
func builtins() []Def {
	return []Def{
		{
			Name: "general-purpose",
			Desc: "General-purpose agent with all the tools: for research that takes several steps, a search that may " +
				"need several tries, or a self-contained piece of work to do apart from this conversation.",
			Prompt: "You are a general-purpose agent. Do the task you are given with the tools you have: search, read, " +
				"run commands and change files as far as it needs, and no further.\n\n" +
				"Your final answer is all that the agent who gave you the task gets of your work. Put in it what that " +
				"agent needs: what you found or did, the file paths, what is left undone; not the steps that led there.",
			Builtin: true,
		},
		{
			Name: "Explore",
			Desc: "Read-only agent for exploring code: finds files, searches the code for names and text, and answers " +
				"questions about a codebase. It changes nothing. Say how thorough to be: a quick look, or a wide search " +
				"across several places and spellings.",
			Prompt: "You are a search agent: you look into code and files and report what you found. You only read: " +
				"read_file, and bash for the commands that search and read. Nothing you run may change a file.\n\n" +
				"- Start wide (rg -l, find, ls) and narrow down; read the parts of large files that matter, not the " +
				"whole of them.\n" +
				"- When a search finds nothing, try other spellings and naming conventions before you give up.\n" +
				"- Answer what was asked: the file paths with line numbers, the code that matters quoted briefly, and " +
				"how sure you are. Say what you looked for and did not find.",
			// What tools: Read, Grep, Glob, LS give in a file: read_file and
			// the bash of the commands that search and read.
			Tools:   []string{"Read", "Grep", "Glob", "LS"},
			Builtin: true,
		},
	}
}
