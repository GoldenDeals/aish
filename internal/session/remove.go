package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Remove deletes the files of a saved session. An open one is refused:
// its lock is held while the files go, so that no aish opens it meanwhile.
func Remove(dir, id string) error {
	if err := CheckID(id); err != nil {
		return err
	}
	found, err := remove(dir, id)
	if err == nil && !found {
		return fmt.Errorf("no session %s", id)
	}
	return err
}

// remove is Remove that reports a session already gone by found instead
// of an error: two aish may prune at once.
func remove(dir, id string) (found bool, err error) {
	for _, ext := range []string{".jsonl", ".state", ".name", ".lock"} {
		if _, err := os.Lstat(filepath.Join(dir, id+ext)); err == nil {
			found = true
		}
	}
	if !found {
		return false, nil
	}
	l, err := lock(dir, id)
	if err != nil {
		return true, err
	}
	defer unlock(l) // the .lock goes last
	for _, ext := range []string{".jsonl", ".state", ".name"} {
		if err := os.Remove(filepath.Join(dir, id+ext)); err != nil && !os.IsNotExist(err) {
			return true, err
		}
	}
	return true, nil
}

// Prune removes the sessions not modified for ttl and returns their IDs;
// open ones are kept. ttl 0 takes every session that is not open, a
// negative one none: sessions_ttl = "0", which is off, is the caller's to
// tell from --older 0d.
func Prune(dir string, ttl time.Duration) ([]string, error) {
	if ttl < 0 {
		return nil, nil
	}
	list, err := List(dir)
	if err != nil {
		return nil, err
	}
	before := time.Now().Add(-ttl)
	var removed []string
	var errs []error
	for _, i := range list {
		if i.Open || i.Modified.After(before) {
			continue
		}
		found, err := remove(dir, i.ID)
		if err != nil {
			errs = append(errs, err)
		} else if found {
			removed = append(removed, i.ID)
		}
	}
	return removed, errors.Join(errs...)
}
