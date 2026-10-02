package agent

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// defaultMask is what looks like a credential whatever command printed
// it. A pattern with a capturing group hides only the group: the key=value
// one keeps the key, the private-key one keeps the BEGIN/END lines, so
// the model still sees what kind of secret was there.
var defaultMask = []string{
	`AKIA[0-9A-Z]{16}`,
	`ghp_[A-Za-z0-9]{36}`,
	`github_pat_[A-Za-z0-9_]{82}`,
	`sk-[A-Za-z0-9_-]{20,}`,
	`xox[baprs]-[A-Za-z0-9-]{10,}`,
	`-----BEGIN [A-Z ]*PRIVATE KEY-----\s*([\s\S]*?)\s*-----END [A-Z ]*PRIVATE KEY-----`,
	`(?i)(?:api[_-]?key|secret(?:[_-]?access)?(?:[_-]?key)?|access[_-]?key|token|password|passwd)\s*[=:]\s*["']?([^\s"',;]{8,})`,
}

// Masker replaces what looks like a secret before text goes to the model.
// A nil Masker masks nothing.
type Masker struct {
	res []*regexp.Regexp
}

// NewMasker compiles the built-in patterns (unless defaults is false) and
// extra ones from the config. The built-in ones go first: a value they
// have hidden is too short for the generic key=value pattern to hit again.
func NewMasker(defaults bool, extra []string) (*Masker, error) {
	var pats []string
	if defaults {
		pats = append(pats, defaultMask...)
	}
	pats = append(pats, extra...)
	m := &Masker{}
	for _, p := range pats {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("mask %q: %w", p, err)
		}
		m.res = append(m.res, re)
	}
	return m, nil
}

// Len is the number of patterns.
func (m *Masker) Len() int {
	if m == nil {
		return 0
	}
	return len(m.res)
}

// Mask hides every match in s: the first capturing group when the pattern
// has one and it matched, the whole match otherwise.
func (m *Masker) Mask(s string) string {
	if m == nil {
		return s
	}
	for _, re := range m.res {
		s = maskWith(re, s)
	}
	return s
}

func maskWith(re *regexp.Regexp, s string) string {
	locs := re.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		from, to := loc[0], loc[1]
		if len(loc) > 2 && loc[2] >= 0 {
			from, to = loc[2], loc[3]
		}
		b.WriteString(s[last:from])
		b.WriteString(hide(s[from:to]))
		last = to
	}
	b.WriteString(s[last:])
	return b.String()
}

// hide keeps the first four characters, enough to tell AKIA from ghp_,
// and drops the rest.
func hide(v string) string {
	n := 0
	for i := 0; i < 4 && n < len(v); i++ {
		_, w := utf8.DecodeRuneInString(v[n:])
		n += w
	}
	return v[:n] + "***"
}
