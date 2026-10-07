package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// builtin is a tool aish does itself, in its own process.
type builtin struct {
	name, desc string
	args       []Arg
	run        func(ctx context.Context, args map[string]any) (string, error)
}

func (t builtin) Name() string           { return t.name }
func (t builtin) Desc() string           { return t.desc }
func (t builtin) Args() []Arg            { return t.args }
func (t builtin) Schema() map[string]any { return Schema(t.args) }

// Execute takes a relative path from ex.Dir: the agent runs in the proxy,
// whose directory is not the shell's. The path is cleaned as the policy
// cleans it (policy.NewInput), so that /home/u/link/../x is not checked as
// /home/u/x and written wherever link leads.
func (t builtin) Execute(ctx context.Context, ex Exec, args map[string]any, _ io.Writer) (string, error) {
	if p, ok := args["path"].(string); ok && p != "" {
		q := homePath(p)
		if ex.Dir != "" && !filepath.IsAbs(q) {
			q = filepath.Join(ex.Dir, q)
		}
		if q = filepath.Clean(q); q != p {
			args = maps.Clone(args)
			args["path"] = q
		}
	}
	return t.run(ctx, args)
}

// shell is bash: the agent does not run it, it hands the command to the
// user's live shell.
type shell struct {
	desc string
	args []Arg
}

func (shell) Name() string                  { return Bash }
func (t shell) Desc() string                { return t.desc }
func (t shell) Args() []Arg                 { return t.args }
func (t shell) Schema() map[string]any      { return Schema(t.args) }
func (shell) Title(a map[string]any) string { return str(a, "command") }

func (shell) Command(args map[string]any) (string, bool) {
	cmd := str(args, "command")
	return cmd, strings.TrimSpace(cmd) != ""
}

func (shell) Execute(context.Context, Exec, map[string]any, io.Writer) (string, error) {
	return "", fmt.Errorf("tool %s runs in the user's shell, not here", Bash)
}

// dialog is ask_user: a call is questions for the user, which the agent
// asks on its terminal. The schema is the model's, with what Args cannot
// say: the questions are objects, and how many of them.
type dialog struct{ desc string }

const askUser = "ask_user"

func (dialog) Name() string   { return askUser }
func (t dialog) Desc() string { return t.desc }
func (dialog) Dialog() bool   { return true }

func (dialog) Args() []Arg {
	return []Arg{{Name: "questions", Type: "array", Desc: "Questions to ask, 1 to 4, as JSON", Required: true}}
}

func (dialog) Schema() map[string]any {
	prop := func(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }
	option := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"label":       prop("string", "The choice as the user sees it and the answer names it, in 1-5 words"),
			"description": prop("string", "What the choice means or implies"),
		},
		"required": []string{"label"},
	}
	question := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question": prop("string", "The complete question, ending with a question mark"),
			"header":   prop("string", `A short label of the question, at most 12 characters: its title and the name of its answer, e.g. "Approach"`),
			"options": map[string]any{
				"type": "array", "minItems": 2, "maxItems": 4, "items": option,
				"description": `2 to 4 choices; "Other", for an answer of the user's own, is added to them`,
			},
			"multi_select": prop("boolean", "Let the user pick several options"),
		},
		"required": []string{"question", "header", "options"},
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"questions": map[string]any{
				"type": "array", "minItems": 1, "maxItems": 4, "items": question,
				"description": "1 to 4 questions; the user answers them one by one",
			},
		},
		"required": []string{"questions"},
	}
}

// Title names the questions by their headers.
func (dialog) Title(args map[string]any) string {
	qs, _ := args["questions"].([]any)
	var hs []string
	for _, q := range qs {
		if m, ok := q.(map[string]any); ok {
			if h := strings.TrimSpace(str(m, "header")); h != "" {
				hs = append(hs, h)
			}
		}
	}
	return strings.TrimSpace(askUser + " " + strings.Join(hs, ", "))
}

func (dialog) Execute(context.Context, Exec, map[string]any, io.Writer) (string, error) {
	return "", fmt.Errorf("tool %s asks on the agent's terminal, not here", askUser)
}

// external is a user's tool: an executable whose header describes it.
type external struct {
	name, desc, path string
	args             []Arg
}

func (t external) Name() string           { return t.name }
func (t external) Desc() string           { return t.desc }
func (t external) Args() []Arg            { return t.args }
func (t external) Schema() map[string]any { return Schema(t.args) }
func (t external) Streaming() bool        { return true }

// Execute runs the tool in ex.Dir with ex.Env.
func (t external) Execute(ctx context.Context, ex Exec, args map[string]any, live io.Writer) (string, error) {
	return runExternal(ctx, t, ex, args, live)
}

func Builtins() []Tool {
	return []Tool{
		shell{
			desc: "Executes a given bash command in the user's interactive bash session and returns its output.\n\n" +
				"The session is the user's own shell: state persists between calls (cwd, exported variables, functions), " +
				"the user's aliases and functions are available, the user sees the command, its output is folded on their screen.\n\n" +
				"IMPORTANT: Avoid using this tool to run cat, head, tail, sed, awk or echo to read, edit or write files. " +
				"Instead, use read_file, edit_file and write_file. Output text to the user directly, not with echo/printf.\n\n" +
				"Usage:\n" +
				"- Stdin is /dev/null: do not run interactive programs (editors, pagers, prompts); use non-interactive flags.\n" +
				"- Never run `exit`, `exec` or `logout`: it would close the user's shell.\n" +
				"- Avoid `cd` and use absolute paths: a cd stays in effect for the user. Unless the user asked to go somewhere, return in the same command " +
				"(`cd dir && make; cd -`, or a subshell: `(cd dir && make)`). Never cd into the directory the shell is already in.\n" +
				"- Always quote file paths that contain spaces with double quotes.\n" +
				"- Do not run long-running servers or watchers in the foreground; start them in the background with output redirected to a file.\n" +
				"- Do not sleep between commands that can run immediately — just run them.\n" +
				"- When issuing multiple commands that depend on each other, chain them with '&&'; use ';' only when you don't care if earlier commands fail.",
			args: []Arg{{Name: "command", Type: "string", Desc: "Bash command line to execute", Required: true}},
		},
		builtin{
			name: "read_file",
			desc: "Reads a file from the local filesystem. You can access any file directly by using this tool. " +
				"It is okay to read a file that does not exist; an error will be returned.\n\n" +
				"Usage:\n" +
				"- By default, it reads up to 2000 lines starting from the beginning of the file. " +
				"For large files, read the part you need with offset and limit.\n" +
				"- Results are returned with line numbers: spaces, the line number, a tab, then the line content.\n" +
				"- This tool can only read text files, not directories. To list files in a directory, use bash.",
			args: []Arg{
				{Name: "path", Type: "string", Desc: "File path, absolute or relative to the shell's cwd", Required: true},
				{Name: "offset", Type: "integer", Desc: "First line to read, 1-based (default 1)"},
				{Name: "limit", Type: "integer", Desc: "Maximum number of lines (default 2000)"},
			},
			run: readFile,
		},
		builtin{
			name: "write_file",
			desc: "Writes a file to the local filesystem, overwriting if one exists. Parent directories are created.\n\n" +
				"Usage:\n" +
				"- If this is an existing file, you MUST use read_file first to read its contents.\n" +
				"- Prefer edit_file for modifying existing files — it only sends the diff. Only use this tool to create new files or for complete rewrites.\n" +
				"- NEVER create documentation files (*.md) or README files unless explicitly requested by the user.\n" +
				"- Only use emojis if the user explicitly requests it. Avoid writing emojis to files unless asked.",
			args: []Arg{
				{Name: "path", Type: "string", Desc: "File path", Required: true},
				{Name: "content", Type: "string", Desc: "Full new content of the file", Required: true, Stdin: true},
			},
			run: writeFile,
		},
		builtin{
			name: "edit_file",
			desc: "Performs exact string replacements in files.\n\n" +
				"Usage:\n" +
				"- You must use read_file at least once before editing a file.\n" +
				"- When editing text from read_file output, preserve the exact indentation (tabs/spaces) as it appears AFTER the line number prefix " +
				"(spaces, line number, tab). Never include any part of the line number prefix in old_string or new_string.\n" +
				"- ALWAYS prefer editing existing files. NEVER write new files unless explicitly required.\n" +
				"- Keep old_string minimal — usually 1-3 lines, only enough to be unique in the file.\n" +
				"- The edit will FAIL if old_string is not unique in the file. In that case, add the minimum extra context needed for uniqueness, " +
				"or use replace_all to change every instance.\n" +
				"- Use replace_all for replacing and renaming strings across the file.",
			args: []Arg{
				{Name: "path", Type: "string", Desc: "File path", Required: true},
				{Name: "old_string", Type: "string", Desc: "Exact text to replace, including whitespace", Required: true},
				{Name: "new_string", Type: "string", Desc: "Replacement text", Required: true},
				{Name: "replace_all", Type: "boolean", Desc: "Replace every occurrence"},
			},
			run: editFile,
		},
		dialog{
			desc: "Asks the user questions on their terminal and returns the answers. " +
				"Each question offers 2 to 4 options: the user picks one, or several with multi_select, " +
				"or types an answer of their own under \"Other\", which is always added.\n\n" +
				"Use it when the request is ambiguous in a way that matters, or to choose between approaches " +
				"that neither the request nor the code decides. Do not use it to ask for permission " +
				"(the user's policy decides what may run), instead of reading the code, " +
				"or for what the user has already said.\n\n" +
				"Usage:\n" +
				"- Ask up to 4 related questions in one call rather than a call for each.\n" +
				"- If you recommend an option, put it first and add \"(Recommended)\" to its label.\n" +
				"- Do not add an \"Other\" option yourself.\n" +
				"- The result has a line \"header: answer\" per question, several options joined with \", \". " +
				"If the user cancels, the call fails: stop and wait for their next request, do not ask again.",
		},
	}
}

func readFile(_ context.Context, args map[string]any) (string, error) {
	path := str(args, "path")
	if err := notRegular(path); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	offset, limit := max(num(args, "offset"), 1), num(args, "limit")
	if limit <= 0 {
		limit = 2000
	}
	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	n, shown := 0, 0
	for sc.Scan() {
		n++
		if n < offset {
			continue
		}
		if shown == limit {
			fmt.Fprintf(&b, "[... more lines; continue with offset=%d]\n", n)
			break
		}
		line := sc.Text()
		if len(line) > 2000 {
			// Not through the middle of a rune: the result goes to the
			// model as JSON, where broken UTF-8 turns into U+FFFD.
			cut := 2000
			for cut > 2000-utf8.UTFMax+1 && !utf8.RuneStart(line[cut]) {
				cut--
			}
			line = line[:cut] + "[...]"
		}
		fmt.Fprintf(&b, "%6d\t%s\n", n, line)
		shown++
	}
	if err := sc.Err(); err != nil {
		return b.String(), err
	}
	if n == 0 {
		return "(empty file)", nil
	}
	return b.String(), nil
}

func writeFile(_ context.Context, args map[string]any) (string, error) {
	path, content := str(args, "path"), str(args, "content")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	if err := writeAtomic(path, []byte(content), mode); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
}

func editFile(_ context.Context, args map[string]any) (string, error) {
	path, old, repl := str(args, "path"), str(args, "old_string"), str(args, "new_string")
	if old == "" {
		return "", fmt.Errorf("old_string is empty")
	}
	if err := notRegular(path); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	n := bytes.Count(data, []byte(old))
	switch {
	case n == 0:
		return "", fmt.Errorf("old_string not found in %s", path)
	case n > 1 && !boolean(args, "replace_all"):
		return "", fmt.Errorf("old_string occurs %d times in %s; add context or set replace_all", n, path)
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	out := bytes.ReplaceAll(data, []byte(old), []byte(repl))
	if err := writeAtomic(path, out, st.Mode().Perm()); err != nil {
		return "", err
	}
	return fmt.Sprintf("replaced %d occurrence(s) in %s", n, path), nil
}

// runExternal runs a user tool with the command line ParseCLI reads: the
// positional arguments first, so they are always $1, $2…, then the flags as
// --name VALUE (a true boolean as --name); a Stdin argument on standard
// input. Every argument given is also in AISH_ARG_<NAME>. Output is shown
// live and returned.
func runExternal(ctx context.Context, t external, ex Exec, args map[string]any, live io.Writer) (string, error) {
	var pos, flags []string
	cmd := exec.CommandContext(ctx, t.path)
	cmd.Dir = ex.Dir
	cmd.Env = ex.Env
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	for _, a := range t.args {
		v := args[a.Name]
		if v == nil {
			// A missing positional argument would shift the rest.
			if a.Required {
				return "", fmt.Errorf("%s: missing argument %s", t.name, a.Name)
			}
			continue
		}
		s := cliValue(v)
		cmd.Env = append(cmd.Env, "AISH_ARG_"+strings.ToUpper(a.Name)+"="+s)
		switch {
		case a.Stdin:
			cmd.Stdin = strings.NewReader(s)
		case a.Flag && a.Type == "boolean":
			if boolean(args, a.Name) {
				flags = append(flags, "--"+a.Name)
			}
		case a.Flag:
			flags = append(flags, "--"+a.Name, s)
		default:
			pos = append(pos, s)
		}
	}
	cmd.Args = append(append(cmd.Args, pos...), flags...)
	var buf bytes.Buffer
	w := io.Writer(&buf)
	if live != nil {
		w = io.MultiWriter(&buf, live)
	}
	cmd.Stdout, cmd.Stderr = w, w
	err := cmd.Run()
	if err != nil {
		return buf.String(), fmt.Errorf("%s: %w", t.name, err)
	}
	return buf.String(), nil
}
