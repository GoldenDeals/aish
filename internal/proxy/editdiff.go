package proxy

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/GoldenDeals/aish/internal/capture"
)

// editsDir, in $AISH_RUN, is where the neovim plugin of aish
// (contrib/nvim) leaves the diff of the files it saved, a file NAME.diff
// each time neovim exits or is stopped. neovim is a full-screen program:
// its command is one line in the journal, and the model would not know
// what the user changed. A file, not a call over $AISH_SOCK: neovim's exit
// waits for nothing, and the diff is there by the time the shell is back
// at its prompt, which is when the command goes to the journal, as the
// shell's state does.
const editsDir = "edits"

// editsNote heads each diff in the command's output.
const editsNote = "[the user saved these changes in neovim; a diff against the files before]"

// dropEdits removes the diffs no command took, at the start of the user's
// command: those of the agent's commands, of a neovim run outside the
// shell's commands. What is left is the command's own. Called under p.mu.
func (p *Proxy) dropEdits() {
	for _, f := range p.editFiles() {
		_ = os.Remove(f)
	}
}

// withEdits adds the diffs the user's command left to out, its output, and
// removes them. With them the output is no longer the one line of a
// full-screen program: it is kept and sent as any output is, the model
// getting max_output_bytes of it. Called under p.mu.
func (p *Proxy) withEdits(out string, tui bool) (string, bool) {
	files := p.editFiles()
	if len(files) == 0 {
		return out, tui
	}
	limit := headCap + tailCap
	var parts []string
	for _, f := range files {
		d := strings.TrimRight(readEdits(f, limit), "\n")
		_ = os.Remove(f)
		if strings.TrimSpace(d) != "" {
			parts = append(parts, editsNote+"\n"+d)
		}
	}
	if len(parts) == 0 {
		return out, tui
	}
	edits := capture.Truncate(strings.Join(parts, "\n"), limit)
	if out != "" {
		out += "\n"
	}
	return out + edits, false
}

// editFiles are the diffs in editsDir, oldest first. Only regular files:
// opening a FIFO there would hang the proxy, which holds p.mu.
func (p *Proxy) editFiles() []string {
	if p.run == "" {
		return nil
	}
	dir := filepath.Join(p.run, editsDir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type diff struct {
		path string
		mod  time.Time
	}
	var ds []diff
	for _, e := range ents {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".diff") {
			continue // the plugin's .tmp, being written
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		ds = append(ds, diff{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	slices.SortFunc(ds, func(a, b diff) int {
		if c := a.mod.Compare(b.mod); c != 0 {
			return c
		}
		return strings.Compare(a.path, b.path)
	})
	paths := make([]string, len(ds))
	for i, d := range ds {
		paths[i] = d.path
	}
	return paths
}

// readEdits is the text of a diff, its head and tail when it is over limit
// bytes, as capture.Truncate keeps them, read without the middle. Lines of
// a file in another encoding are not UTF-8: they are made so here, or
// capture.Truncate, which cuts at a valid end, would cut them all.
func readEdits(path string, limit int) string {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return ""
	}
	size := st.Size()
	if size <= int64(limit) {
		b, _ := io.ReadAll(io.LimitReader(f, int64(limit)))
		return strings.ToValidUTF8(string(b), "�")
	}
	head := make([]byte, limit*2/5)
	tail := make([]byte, limit-len(head))
	if _, err := f.ReadAt(head, 0); err != nil {
		return ""
	}
	if _, err := f.ReadAt(tail, size-int64(len(tail))); err != nil {
		return ""
	}
	return fmt.Sprintf("%s\n[... %d bytes omitted ...]\n%s", strings.ToValidUTF8(string(head), "�"),
		size-int64(len(head))-int64(len(tail)), strings.ToValidUTF8(string(tail), "�"))
}
