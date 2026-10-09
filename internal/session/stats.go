package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// What the sessions of a directory spent over the last day, week, month
// and quarter, for `aish stats`: tokens, requests to the agent and
// sessions, in total and by model. A request is a KindUser entry, counted
// for the model of the first turn that answers it. Tokens are those of the
// agent's turns and of its subagents' (KindUsage), counted as InputTokens,
// CachedTokens and OutputTokens are: what was sent, cache included, the
// part of it read from the cache, the reply. A session counts in a period
// it has a request or a measured turn in; for a model, a request it
// answered or a turn of its.
//
// Each run reads the journals afresh, with no index of its own to go
// stale. A journal not written since the longest period began is not
// opened: no entry is newer than its file. Of the others only the lines of
// requests, turns and usage are decoded, and those of commands, tool
// results and files, the bulk of a journal, are told by their first bytes
// and skipped without being kept whole.

// Period is a stretch of time up to now: the last Span.
type Period struct {
	Name string
	Span time.Duration
}

// Periods are those aish stats shows.
var Periods = []Period{
	{"24 hours", 24 * time.Hour},
	{"7 days", 7 * 24 * time.Hour},
	{"30 days", 30 * 24 * time.Hour},
	{"90 days", 90 * 24 * time.Hour},
}

// Spend is what was spent in a period.
type Spend struct {
	Requests int
	Sessions int
	Input    int // tokens sent, those read from the cache included
	Cached   int
	Output   int
}

// Stats is the spend in each of its periods, in total and by model.
type Stats struct {
	Periods []Period
	Total   []Spend // a Spend for each period
	// Models are those with spend in a period, the most tokens over the
	// longest period first.
	Models []ModelSpend
}

// ModelSpend is the spend of one model in each period.
type ModelSpend struct {
	Model string
	Spend []Spend
}

// unknownModel names the model of a turn that names none.
const unknownModel = "(unknown)"

// CollectStats sums up the journals of dir over periods up to now. A
// journal it cannot read is left out, and the error says which.
func CollectStats(dir string, periods []Period, now time.Time) (Stats, error) {
	st := Stats{Periods: periods, Total: make([]Spend, len(periods))}
	if len(periods) == 0 {
		return st, nil
	}
	c := collector{periods: periods, now: now, total: st.Total, models: map[string][]Spend{}}
	for i, p := range periods {
		if p.Span > periods[c.longest].Span {
			c.longest = i
		}
	}
	from := now.Add(-periods[c.longest].Span)
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return st, err
	}
	var errs []error
	for _, f := range files {
		if CheckID(trimExt(filepath.Base(f))) != nil {
			continue // no session's
		}
		fi, err := os.Stat(f)
		switch {
		case os.IsNotExist(err): // removed meanwhile
		case err != nil:
			errs = append(errs, err)
		case !fi.ModTime().Before(from):
			if err := c.journal(f); err != nil {
				errs = append(errs, err)
			}
		}
	}
	for m, sp := range c.models {
		st.Models = append(st.Models, ModelSpend{Model: m, Spend: sp})
	}
	sort.Slice(st.Models, func(i, j int) bool {
		a, b := st.Models[i], st.Models[j]
		x, y := a.Spend[c.longest], b.Spend[c.longest]
		if tx, ty := x.Input+x.Output, y.Input+y.Output; tx != ty {
			return tx > ty
		}
		return a.Model < b.Model
	})
	return st, errors.Join(errs...)
}

type collector struct {
	periods []Period
	longest int // the index of the longest period
	now     time.Time
	total   []Spend
	models  map[string][]Spend
}

// within tells whether t is in period i.
func (c *collector) within(t time.Time, i int) bool {
	return !t.Before(c.now.Add(-c.periods[i].Span))
}

// statLine is what Stats reads of an entry.
type statLine struct {
	Kind         string    `json:"kind"`
	Time         time.Time `json:"time"`
	Model        string    `json:"model"`
	InputTokens  int       `json:"input_tokens"`
	CachedTokens int       `json:"cached_tokens"`
	OutputTokens int       `json:"output_tokens"`
	Usage        *Usage    `json:"usage"`
}

// journal adds the session of the journal at path.
func (c *collector) journal(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	s := sessionTally{c: c, active: make([]bool, len(c.periods)), models: map[string][]bool{}}
	var asked *time.Time // a request no turn of a model has answered yet
	err = eachLine(f, statWanted, func(line []byte) {
		var e statLine
		if json.Unmarshal(line, &e) != nil {
			return // as Open skips it
		}
		switch e.Kind {
		case KindUser:
			s.request(e.Time)
			asked = &e.Time
		case KindAssistant:
			if e.Model != "" && asked != nil {
				s.answered(*asked, e.Model)
				asked = nil
			}
			s.spent(e.Time, e.Model, Usage{Input: e.InputTokens, Cached: e.CachedTokens, Output: e.OutputTokens})
		case KindUsage:
			if e.Usage != nil {
				s.spent(e.Time, e.Model, *e.Usage)
			}
		}
	})
	s.done()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// sessionTally is one journal's part of a collector: the periods, in total
// and for each model, the session counts in.
type sessionTally struct {
	c      *collector
	active []bool
	models map[string][]bool
}

// model is the spend of model m in each period, and where the session
// counts for it.
func (s *sessionTally) model(m string) ([]Spend, []bool) {
	if m == "" {
		m = unknownModel
	}
	sp := s.c.models[m]
	if sp == nil {
		sp = make([]Spend, len(s.c.periods))
		s.c.models[m] = sp
	}
	in := s.models[m]
	if in == nil {
		in = make([]bool, len(s.c.periods))
		s.models[m] = in
	}
	return sp, in
}

func (s *sessionTally) request(t time.Time) {
	for i := range s.c.periods {
		if s.c.within(t, i) {
			s.c.total[i].Requests++
			s.active[i] = true
		}
	}
}

// answered counts the request made at t for model m.
func (s *sessionTally) answered(t time.Time, m string) {
	if !s.c.within(t, s.c.longest) {
		return
	}
	sp, in := s.model(m)
	for i := range s.c.periods {
		if s.c.within(t, i) {
			sp[i].Requests++
			in[i] = true
		}
	}
}

// spent counts a turn of model m at t that cost u.
func (s *sessionTally) spent(t time.Time, m string, u Usage) {
	if u.Input == 0 && u.Output == 0 || !s.c.within(t, s.c.longest) {
		return // cut off, or a provider that counts nothing
	}
	sp, in := s.model(m)
	for i := range s.c.periods {
		if !s.c.within(t, i) {
			continue
		}
		for _, x := range []*Spend{&s.c.total[i], &sp[i]} {
			x.Input += u.Input
			x.Cached += u.Cached
			x.Output += u.Output
		}
		s.active[i], in[i] = true, true
	}
}

// done counts the session in the periods it was active in.
func (s *sessionTally) done() {
	for i, on := range s.active {
		if on {
			s.c.total[i].Sessions++
		}
	}
	for m, in := range s.models {
		for i, on := range in {
			if on {
				s.c.models[m][i].Sessions++
			}
		}
	}
}

// statWanted tells by the first bytes of a journal line whether Stats
// reads it: a request, a turn, a subagent's usage; or a line Append did
// not write, whose kind is not where Append puts it.
func statWanted(head []byte) bool {
	rest, ok := bytes.CutPrefix(head, []byte(`{"kind":"`))
	if !ok {
		return true
	}
	for _, k := range []string{KindUser, KindAssistant, KindUsage} {
		if bytes.HasPrefix(rest, []byte(k+`"`)) {
			return true
		}
	}
	return false
}

// eachLine calls fn with each line of r that want takes by its first
// bytes; a line it does not take is read past, not kept. fn must not keep
// the line.
func eachLine(r io.Reader, want func(head []byte) bool, fn func(line []byte)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		keep := want(chunk)
		line = line[:0]
		for {
			if keep {
				line = append(line, chunk...)
			}
			if err != bufio.ErrBufferFull {
				break
			}
			chunk, err = br.ReadSlice('\n')
		}
		if keep && len(bytes.TrimSpace(line)) > 0 {
			fn(line)
		}
		switch {
		case err == io.EOF:
			return nil
		case err != nil:
			return err
		}
	}
}
