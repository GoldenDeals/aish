package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// A project file may name directories of hooks and tools: code from the
// repository, which a cloned one would have aish run on its first request
// there. Those keys hold only for a file the user trusted, as it was then:
// the trust is in its path and a sha256 of its contents and of the hooks
// and tools it names (codeSum), so an edit of any, a git pull with one
// say, takes it back until `aish trust` is run again.

// TrustFile is where the trusted project files are kept: their paths and
// the codeSum trusted.
func TrustFile() string { return filepath.Join(dataDir(), "trusted.json") }

// codeKey is a key of a project file that runs code from the repository.
type codeKey struct {
	name   string
	value  *string // as the file sets it, nil if it does not
	dst    *string // the field of Config it is added to
	events bool    // the code is in a directory per event, as hooks.Find reads it
}

// code are the keys of pr that run code, to be laid over cfg.
func (pr *project) code(cfg *Config) []codeKey {
	return []codeKey{
		{"hooks_dir", pr.HooksDir, &cfg.HooksDir, true},
		{"tools_dir", pr.ToolsDir, &cfg.ToolsDir, false},
	}
}

// CodeKeys are the keys of the project file at path that run code from
// the repository, as lines key = "value" the way the file sets them: what
// trusting it lets in.
func CodeKeys(path string) ([]string, error) {
	var pr project
	if _, err := toml.DecodeFile(path, &pr); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var keys []string
	for _, k := range pr.code(&Config{}) {
		if k.value != nil {
			keys = append(keys, k.name+" = "+strconv.Quote(*k.value))
		}
	}
	return keys, nil
}

// Trusted tells whether path, as it is now, was trusted.
func Trusted(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && trusted(path, b)
}

// trusted tells whether data, read from path, is what was trusted there.
func trusted(path string, data []byte) bool {
	key, err := trustKey(path)
	if err != nil {
		return false
	}
	want, ok := TrustedFiles()[key]
	if !ok {
		return false
	}
	sum, err := codeSum(path, data)
	return err == nil && sum == want
}

// Trust records path with the codeSum of it and its code now.
func Trust(path string) error {
	key, err := trustKey(path)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sum, err := codeSum(path, b)
	if err != nil {
		return err
	}
	all := TrustedFiles()
	all[key] = sum
	return saveTrust(all)
}

// Untrust forgets path.
func Untrust(path string) error {
	key, err := trustKey(path)
	if err != nil {
		return err
	}
	all := TrustedFiles()
	if _, ok := all[key]; !ok {
		return nil
	}
	delete(all, key)
	return saveTrust(all)
}

// TrustedFiles are the files trusted, by path, with the codeSum trusted.
// A TrustFile that cannot be read, or is broken, means none, without an
// error: refusing is safe.
func TrustedFiles() map[string]string {
	all := map[string]string{}
	b, err := os.ReadFile(TrustFile())
	if err != nil || json.Unmarshal(b, &all) != nil || all == nil {
		return map[string]string{}
	}
	return all
}

// Sum is the codeSum of the file at path, as trust records it; "" if it,
// or a file of the code it names, cannot be read.
func Sum(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum, err := codeSum(path, b)
	if err != nil {
		return ""
	}
	return sum
}

// codeSum is the sha256, in hex, of data, the contents of the project file
// at path, and of the code in the directories its hooks_dir and tools_dir
// name, taken as Project adds them (a value may be a list). The files of
// each directory are summed as tools.Load takes them, and for hooks_dir
// those of the directories in it too, as hooks.Find takes each event's:
// every one by its name, executable bits and contents. A file that is not
// executable is one chmod away from running, and one beside the hooks
// may be a library they source. A link is summed as what it points to,
// which is what runs; a directory reached by a link is not read, nor one
// deeper: neither runs anything from those. A directory that is not
// there, or cannot be listed, runs nothing: it has no files, and once it
// does the sum changes. A file that cannot be read may run all the same,
// so it is an error, and nothing is trusted.
func codeSum(path string, data []byte) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "%s %s\n", ProjectFile, sha(data))
	var pr project
	if _, err := toml.Decode(string(data), &pr); err != nil {
		return hex.EncodeToString(h.Sum(nil)), nil // Project refuses it: no code runs
	}
	base := filepath.Dir(path)
	for _, k := range pr.code(&Config{}) {
		if k.value == nil {
			continue
		}
		for i, dir := range filepath.SplitList(addDir("", *k.value, base)) {
			// Not dir itself: the file is the same reached through a link
			// to its directory (trustKey), and the value is in data.
			fmt.Fprintf(h, "%s %d\n", k.name, i)
			if err := sumDir(h, dir, "", k.events); err != nil {
				return "", err
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sumDir writes to h a line for each file in dir, named rel/<name>, and
// with deep for those of the directories in it but the ones starting with
// a dot, which hooks.Find skips.
func sumDir(h io.Writer, dir, rel string, deep bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		full, name := filepath.Join(dir, e.Name()), e.Name()
		if rel != "" {
			name = rel + "/" + name
		}
		if e.IsDir() { // not a link to one, which hooks.Find skips as a file
			if deep && !strings.HasPrefix(e.Name(), ".") {
				if err := sumDir(h, full, name, false); err != nil {
					return err
				}
			}
			continue
		}
		st, err := os.Stat(full)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		sum, err := fileSum(full)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%q %03o %s\n", name, st.Mode()&0o111, sum)
	}
	return nil
}

// fileSum is the sha256 of the file at path in hex.
func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// trustKey is path absolute, with the symlinks of its directory resolved:
// a repository reached by another way is the same. The file's own link is
// not resolved: the directories it names are taken from the one it is in,
// and a link to a file trusted elsewhere must not bring that trust to
// another repository's hooks.
func trustKey(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.Base(abs)), nil
}

// saveTrust writes all to TrustFile at once: a crash halfway must not
// leave it broken, which would forget every file trusted.
func saveTrust(all map[string]string) error {
	b, err := json.MarshalIndent(all, "", "\t")
	if err != nil {
		return err
	}
	path := TrustFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o600)
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
