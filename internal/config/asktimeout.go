package config

import "time"

// AskMaxWait is ask_timeout as a duration: 0 waits for ever, not "no time
// to answer".
func (c Config) AskMaxWait() time.Duration {
	d, _ := ParseAge(c.AskTimeout) // check and checkProfiles have made sure it parses
	return d
}
