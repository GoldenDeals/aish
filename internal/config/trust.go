package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/BurntSushi/toml"
)

// A project file may name directories of hooks and tools: code from the
// repository, which a cloned one would have aish run on its first request
// there. Those keys hold only for a file the user trusted, as it was then:
// the trust is in its path and the sha256 of its contents, so an edit, a
// git pull with one say, takes it back until `aish trust` is run again.

// TrustFile is where the trusted project files are kept: their paths and
// the sha256 of the contents trusted.
func TrustFile() string { return filepath.Join(dataDir(), "trusted.json") }

// codeKey is a key of a project file that runs code from the repository.
type codeKey struct {
	name  string
	value *string // as the file sets it, nil if it does not
	dst   *string // the field of Config it is added to
}

// code are the keys of pr that run code, to be laid over cfg.
func (pr *project) code(cfg *Config) []codeKey {
	return []codeKey{
		{"hooks_dir", pr.HooksDir, &cfg.HooksDir},
		{"tools_dir", pr.ToolsDir, &cfg.ToolsDir},
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
	sum, ok := TrustedFiles()[key]
	return ok && sum == sha(data)
}

// Trust records path with the sha256 of its contents now.
func Trust(path string) error {
	key, err := trustKey(path)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	all := TrustedFiles()
	all[key] = sha(b)
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

// TrustedFiles are the files trusted, by path, with the sha256 of their
// contents trusted. A TrustFile that cannot be read, or is broken, means
// none, without an error: refusing is safe.
func TrustedFiles() map[string]string {
	all := map[string]string{}
	b, err := os.ReadFile(TrustFile())
	if err != nil || json.Unmarshal(b, &all) != nil || all == nil {
		return map[string]string{}
	}
	return all
}

// Sum is the sha256 of the file at path in hex, as trust records it; ""
// if it cannot be read.
func Sum(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return sha(b)
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
