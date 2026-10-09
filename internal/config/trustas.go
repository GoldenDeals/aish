package config

import "fmt"

// TrustAs records path as Trust does, with sum, the Sum it had when the
// user was asked about it, and only while it and its code still sum to
// that: what is trusted is what the user saw, not what an edit made of it
// while the question was open.
func TrustAs(path, sum string) error {
	key, err := trustKey(path)
	if err != nil {
		return err
	}
	if now := Sum(path); sum == "" || now != sum {
		return fmt.Errorf("%s or its hooks and tools changed since; not trusted", path)
	}
	all := TrustedFiles()
	all[key] = sum
	return saveTrust(all)
}
