package session

import "fmt"

// CheckID rejects a session id that is not a plain file name: ids come
// from the command line and over RPC, and become paths and shell words.
// No dot either: a session's files are its id plus a suffix (.jsonl,
// .state, .state.tmp, .name, .lock), and an id like "x.state" would read
// as another session's file. freshID never makes one.
func CheckID(id string) error {
	if id == "" || id[0] == '-' {
		return fmt.Errorf("bad session id %q", id)
	}
	for _, c := range []byte(id) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return fmt.Errorf("bad session id %q", id)
		}
	}
	return nil
}
