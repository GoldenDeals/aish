package session

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// newestClosed is the session of dir whose journal was modified last of
// those no aish has open, "" if there is none: the first of List that is
// not Open. It goes by stat and the locks alone, the newest first, and
// stops at the first one closed: List would summarize every session, its
// journal or <id>.info, for --resume to open just one.
func newestClosed(dir string) string {
	files, err := os.ReadDir(dir)
	if err != nil {
		return "" // no directory yet: nothing to resume
	}
	type journal struct {
		id       string
		modified time.Time
	}
	var found []journal
	for _, f := range files {
		id, ok := strings.CutSuffix(f.Name(), ".jsonl")
		if !ok || CheckID(id) != nil {
			continue // not a journal, or one Load would refuse
		}
		// Stat, not the entry's Lstat: List's Modified is the journal's.
		fi, err := os.Stat(filepath.Join(dir, f.Name()))
		if err != nil {
			continue // removed since it was read, or no file to open
		}
		found = append(found, journal{id, fi.ModTime()})
	}
	slices.SortStableFunc(found, func(a, b journal) int { return b.modified.Compare(a.modified) })
	for _, j := range found {
		if !isOpen(dir, j.id) {
			return j.id
		}
	}
	return ""
}
