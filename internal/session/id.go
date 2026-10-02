package session

import "fmt"

// CheckID rejects a session id that is not a plain file name: ids come
// from the command line and over RPC, and become paths and shell words.
func CheckID(id string) error {
	if id == "" || id[0] == '.' || id[0] == '-' {
		return fmt.Errorf("bad session id %q", id)
	}
	for _, c := range []byte(id) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return fmt.Errorf("bad session id %q", id)
		}
	}
	return nil
}
