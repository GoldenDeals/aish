package config

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// ParseAge reads an age as sessions_ttl and `aish session prune --older`
// take it: whole days (30d) or a Go duration (12h, 90m); "0" is none.
func ParseAge(s string) (time.Duration, error) {
	var d time.Duration
	var err error
	if days, ok := strings.CutSuffix(s, "d"); ok {
		var n int
		n, err = strconv.Atoi(days)
		d = time.Duration(n) * 24 * time.Hour
	} else {
		d, err = time.ParseDuration(s)
	}
	if err != nil || d < 0 {
		return 0, errors.New("want days (30d) or hours (12h)")
	}
	return d, nil
}

// SessionsMaxAge is sessions_ttl as a duration: 0 is off, not "remove
// them all".
func (c Config) SessionsMaxAge() time.Duration {
	d, _ := ParseAge(c.SessionsTTL) // check has made sure it parses
	return d
}
