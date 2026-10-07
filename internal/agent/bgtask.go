package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inebotov/aish/internal/capture"
	"github.com/inebotov/aish/internal/tools"
)

// Subagents in the background: task with background starts them and
// returns at once, and they work on past the call and the request, while
// the model does its part and the user types at the prompt. Their answers
// come only as results of task_wait and task_result, so the journal gets
// them as any result, and the screen gets nothing: between requests the
// prompt is there, and what is printed past the proxy's output would break
// its status. Their output is kept for `aish tasks` (bgview.go). They
// belong to the session: clear, resume and the end of the proxy stop them
// (StopBackground), and task_cancel; Ctrl+C and the end of a request do
// not.

const (
	// maxUnfinished is how many subagents may be in the background at
	// once, running or queued.
	maxUnfinished = 8
	// maxFinished is how many finished ones are kept for their answers;
	// the oldest go first.
	maxFinished = 32
	// stopWait is how long task_cancel and StopBackground wait for the
	// subagents they cancel, so that their commands' process groups are
	// gone when they return.
	stopWait = 2 * time.Second
	// waitDefault and waitMax bound task_wait, in seconds.
	waitDefault = 60
	waitMax     = 600
)

// The states of a subagent in the background.
const (
	bgQueued    = "queued"
	bgRunning   = "running"
	bgOK        = "ok"
	bgError     = "error"
	bgCancelled = "cancelled"
)

// bgSet is the subagents in the background of an Agent.
type bgSet struct {
	mu sync.Mutex
	n  int // the ids given: bg1 to bgN
	// stopped is n when StopBackground last forgot them all.
	stopped  int
	jobs     map[string]*bgJob
	order    []string // the ids of jobs, as started
	finished []string // the ids of the finished jobs, as they finished
	// changed is closed, and made anew, when one finishes: task_wait waits
	// on it.
	changed chan struct{}
	// running counts the goroutines of jobs, forgotten ones included: all
	// of them share maxParallel, and the rest queue, in the order started.
	running int
}

type bgJob struct {
	id, name string
	prompt   string
	state    string
	reply    string
	err      error
	taken    bool // its answer was in a result already
	run      *subRun
	ctx      context.Context // its own, not the request's: that one ends first
	cancel   context.CancelFunc
	done     chan struct{} // closed once it is finished
	out      *bgOutput
}

func (j *bgJob) over() bool { return j.state != bgQueued && j.state != bgRunning }

// answer is j's block of a result, as task gives it, headed by its id too:
// the same subagent may be at work several times.
func (j *bgJob) answer() string {
	status, text := outcome(j.reply, j.err)
	if j.state == bgCancelled {
		status, text = bgCancelled, j.reply
	}
	return block(j.id+" "+j.name, status, text)
}

// bgOutput is where a subagent in the background shows its work: kept in
// memory, as the screen is not its. Its commands write from goroutines of
// their own.
type bgOutput struct {
	mu  sync.Mutex
	buf *capture.Buffer
}

func (o *bgOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.buf.Write(p)
	return len(p), nil
}

// background is the agent's set, made on first use.
func (a *Agent) background() *bgSet {
	a.bgMu.Lock()
	defer a.bgMu.Unlock()
	if a.bg == nil {
		a.bg = &bgSet{jobs: map[string]*bgJob{}, changed: make(chan struct{})}
	}
	return a.bg
}

// madeBackground is the agent's set, nil if no subagent was ever in the
// background: a subagent's own agent has none.
func (a *Agent) madeBackground() *bgSet {
	a.bgMu.Lock()
	defer a.bgMu.Unlock()
	return a.bg
}

// backgroundKnown tells whether a subagent in the background is known:
// at work, or finished with its answer kept.
func (a *Agent) backgroundKnown() bool {
	s := a.madeBackground()
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jobs) > 0
}

// noteBackground tells the user, as a request ends, of the subagents still
// at work in the background: nothing else shows them.
func (a *Agent) noteBackground() {
	s := a.madeBackground()
	if s == nil {
		return
	}
	s.mu.Lock()
	n := s.unfinished()
	s.mu.Unlock()
	what := "1 subagent"
	switch {
	case n == 0:
		return
	case n > 1:
		what = fmt.Sprintf("%d subagents", n)
	}
	fmt.Fprintf(a.UI, "%s[aish: %s still running in the background, see aish tasks]%s\n", dim, what, reset)
}

// StopBackground stops the subagents in the background and forgets them:
// they belong to the session, which clear and resume leave, and their
// commands, in process groups of their own, would outlive aish. It waits
// for them a while, so that those commands are gone when it returns.
func (a *Agent) StopBackground() {
	if s := a.madeBackground(); s != nil {
		s.stop()
	}
}

// unfinished counts those at work or queued. Called under s.mu.
func (s *bgSet) unfinished() int {
	n := 0
	for _, j := range s.jobs {
		if !j.over() {
			n++
		}
	}
	return n
}

// start starts runs in the background and tells of them, a line each, on
// live too: the live output of the call would end empty otherwise.
func (s *bgSet) start(runs []*subRun, live io.Writer) (string, error) {
	s.mu.Lock()
	if n := s.unfinished(); n+len(runs) > maxUnfinished {
		s.mu.Unlock()
		return "", fmt.Errorf("%d more subagents in the background would make %d unfinished, and at most %d may be: "+
			"take the answers of those at work with task_wait, or stop them with task_cancel", len(runs), n+len(runs), maxUnfinished)
	}
	lines := make([]string, len(runs))
	for i, r := range runs {
		s.n++
		j := &bgJob{
			id: "bg" + strconv.Itoa(s.n), name: r.def.Name, prompt: r.prompt, state: bgQueued, run: r,
			done: make(chan struct{}), out: &bgOutput{buf: capture.NewBuffer(subCapture, subCapture)},
		}
		j.ctx, j.cancel = context.WithCancel(context.Background())
		s.jobs[j.id] = j
		s.order = append(s.order, j.id)
		lines[i] = fmt.Sprintf("started %s (%s)", j.id, j.name)
	}
	s.schedule()
	s.mu.Unlock()
	text := strings.Join(lines, "\n")
	if live != nil {
		io.WriteString(live, text+"\n")
	}
	return text, nil
}

// schedule starts the queued jobs, in the order they came, while fewer
// than maxParallel run. Called under s.mu.
func (s *bgSet) schedule() {
	for _, id := range s.order {
		if s.running >= maxParallel {
			return
		}
		if j := s.jobs[id]; j.state == bgQueued {
			j.state = bgRunning
			s.running++
			go s.work(j)
		}
	}
}

func (s *bgSet) work(j *bgJob) {
	defer j.cancel()
	reply, err := runSafe(j.ctx, j.run, nopFinish{j.out})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running--
	s.finish(j, reply, err)
	s.schedule()
}

// finish records how j ended. Called under s.mu.
func (s *bgSet) finish(j *bgJob, reply string, err error) {
	j.reply, j.err, j.run = reply, err, nil
	switch {
	case err == nil:
		j.state = bgOK
	case j.ctx.Err() != nil:
		j.state = bgCancelled
	default:
		j.state = bgError
	}
	close(j.done)
	if s.jobs[j.id] == j { // not forgotten by stop
		s.finished = append(s.finished, j.id)
		for len(s.finished) > maxFinished {
			s.forget(s.finished[0])
		}
	}
	close(s.changed)
	s.changed = make(chan struct{})
}

// halt cancels j and tells whether it is to be waited for: a queued one is
// finished at once. Called under s.mu.
func (s *bgSet) halt(j *bgJob) bool {
	j.cancel()
	if j.state == bgQueued {
		s.finish(j, "", j.ctx.Err())
		return false
	}
	return j.state == bgRunning
}

// forget drops a finished job. Called under s.mu.
func (s *bgSet) forget(id string) {
	delete(s.jobs, id)
	other := func(x string) bool { return x == id }
	s.order = slices.DeleteFunc(s.order, other)
	s.finished = slices.DeleteFunc(s.finished, other)
}

func (s *bgSet) stop() {
	s.mu.Lock()
	var jobs []*bgJob
	for _, id := range slices.Clone(s.order) { // halt may forget the oldest
		if j := s.jobs[id]; j != nil && !j.over() && s.halt(j) {
			jobs = append(jobs, j)
		}
	}
	s.jobs, s.order, s.finished = map[string]*bgJob{}, nil, nil
	s.stopped = s.n
	s.mu.Unlock()
	awaitDone(jobs)
}

// awaitDone waits for jobs to finish, stopWait at most.
func awaitDone(jobs []*bgJob) {
	t := time.NewTimer(stopWait)
	defer t.Stop()
	for _, j := range jobs {
		select {
		case <-j.done:
		case <-t.C:
			return
		}
	}
}

// missing is why id is none of the set's. Called under s.mu.
func (s *bgSet) missing(id string) string {
	if n, err := strconv.Atoi(strings.TrimPrefix(id, "bg")); err == nil && id == "bg"+strconv.Itoa(n) && n > s.stopped && n <= s.n {
		return fmt.Sprintf("%s is no longer kept: only the last %d finished subagents are", id, maxFinished)
	}
	return fmt.Sprintf("unknown id %s: subagents in the background do not outlive aish, clear or resume", id)
}

// check fails on the first of ids the set does not know: the model fixes
// the call.
func (s *bgSet) check(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if s.jobs[id] == nil {
			return errors.New(s.missing(id))
		}
	}
	return nil
}

// wait waits until the subagents ids are finished, or with no ids one of
// those at work, as long as timeout and ctx let it, and returns answers.
// Without ids an answer not taken yet is enough, and so is nothing at work.
// Meanwhile line, if not nil, shows whom it waits for.
func (s *bgSet) wait(ctx context.Context, ids []string, timeout time.Duration, line *waitLine) (string, error) {
	if err := s.check(ids); err != nil {
		return "", err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var tick <-chan time.Time
	if line != nil {
		t := time.NewTicker(waitTick)
		defer t.Stop()
		tick = t.C
	}
	defer line.clear()
loop:
	for {
		s.mu.Lock()
		ready, changed := s.ready(ids), s.changed
		var awaited []string
		if !ready && line != nil {
			awaited = s.awaited(ids)
		}
		s.mu.Unlock()
		if ready {
			break
		}
		line.draw(awaited)
		select {
		case <-changed:
		case <-tick:
		case <-timer.C:
			break loop // not an error: the answers so far
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return s.answers(ids), nil
}

// awaited are the ids wait waits for: those of ids not finished or, with
// none, those at work. Called under s.mu.
func (s *bgSet) awaited(ids []string) []string {
	if len(ids) == 0 {
		ids = s.order
	}
	var out []string
	for _, id := range ids {
		if j := s.jobs[id]; j != nil && !j.over() {
			out = append(out, id)
		}
	}
	return out
}

// ready tells whether wait has what it waits for. Called under s.mu.
func (s *bgSet) ready(ids []string) bool {
	if len(ids) == 0 {
		atWork := false
		for _, j := range s.jobs {
			if j.over() && !j.taken {
				return true
			}
			atWork = atWork || !j.over()
		}
		return !atWork
	}
	for _, id := range ids {
		if j := s.jobs[id]; j != nil && !j.over() {
			return false
		}
	}
	return true
}

// answers are the blocks of ids or, with none, of those at work and those
// whose answers were not taken yet; the answers in them are taken. One at
// work is a heading of its state alone.
func (s *bgSet) answers(ids []string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(ids) == 0 {
		for _, id := range s.order {
			if j := s.jobs[id]; !j.over() || !j.taken {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return "No subagent is at work in the background, and every answer was taken: task_result lists them."
		}
	}
	blocks := make([]string, len(ids))
	for i, id := range ids {
		switch j := s.jobs[id]; {
		case j == nil: // forgotten meanwhile
			blocks[i] = block(id, "unknown", s.missing(id))
		case !j.over():
			blocks[i] = fmt.Sprintf("## %s %s (%s)", j.id, j.name, j.state)
		default:
			blocks[i] = j.answer()
			j.taken = true
		}
	}
	return strings.Join(blocks, "\n\n")
}

// list is a line for each one known, with its state.
func (s *bgSet) list() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.order) == 0 {
		return "No subagents in the background."
	}
	lines := make([]string, len(s.order))
	for i, id := range s.order {
		j := s.jobs[id]
		lines[i] = fmt.Sprintf("%s %s: %s", j.id, j.name, j.state)
	}
	return strings.Join(lines, "\n")
}

// cancel stops the subagents ids and waits for them a while.
func (s *bgSet) cancel(ids []string) (string, error) {
	if len(ids) == 0 {
		return "", errors.New("ids: name the subagents to stop, by the ids task gave them")
	}
	if err := s.check(ids); err != nil {
		return "", err
	}
	s.mu.Lock()
	named := make([]*bgJob, len(ids))
	var stopping []*bgJob
	for i, id := range ids {
		j := s.jobs[id]
		named[i] = j
		if j != nil && !j.over() && s.halt(j) {
			stopping = append(stopping, j)
		}
	}
	s.mu.Unlock()
	awaitDone(stopping)
	s.mu.Lock()
	defer s.mu.Unlock()
	lines := make([]string, len(ids))
	for i, j := range named {
		switch {
		case j == nil:
			lines[i] = s.missing(ids[i])
		case j.state == bgCancelled:
			lines[i] = fmt.Sprintf("cancelled %s (%s)", j.id, j.name)
		case !j.over():
			lines[i] = fmt.Sprintf("%s (%s) is cancelled, and not stopped yet", j.id, j.name)
		default:
			lines[i] = fmt.Sprintf("%s (%s) was finished already: %s; task_result gives its answer", j.id, j.name, j.state)
		}
	}
	return strings.Join(lines, "\n"), nil
}

// The tools of the subagents in the background, beside task.
const (
	taskWait   = "task_wait"
	taskResult = "task_result"
	taskCancel = "task_cancel"
)

var bgToolNames = []string{taskWait, taskResult, taskCancel}

// bgTool is one of them. They are the agent's alone: subTools gives them
// to no subagent, and aish tool does not list them.
type bgTool struct {
	a    *Agent
	name string
}

func (t *bgTool) Name() string { return t.name }

// Streaming is set for task_wait: the live output of its call shows whom
// it waits for, and then its answers, kept for Ctrl+O.
func (t *bgTool) Streaming() bool { return t.name == taskWait }

func (t *bgTool) Desc() string {
	switch t.name {
	case taskWait:
		return "Waits for subagents that task started in the background and returns their answers: a block each, as task " +
			"gives them, headed \"## ID NAME (ok|error|cancelled)\", and for one still at work only the heading " +
			"\"## ID NAME (running|queued)\". With ids it waits for all of them; without, for any one at work, and returns " +
			"the answers not taken yet. It waits timeout seconds at most (60 by default, 600 at most); time running out is " +
			"no error: those at work go on, and a later task_wait or task_result gives their answers."
	case taskResult:
		return "Tells what subagents that task started in the background have now, without waiting: for ids, their answers " +
			"as task_wait gives them; without ids, a line for each one known with its state (queued, running, ok, error, " +
			"cancelled). An answer may be taken again."
	}
	return "Stops subagents that task started in the background, with the commands they run; what they did is lost."
}

func (t *bgTool) Args() []tools.Arg {
	ids := tools.Arg{Name: "ids", Type: "array", Desc: `The ids task gave the subagents, as JSON: ["bg1", …]`}
	switch t.name {
	case taskWait:
		return []tools.Arg{ids, {Name: "timeout", Type: "number", Flag: true,
			Desc: fmt.Sprintf("Seconds to wait at most: %d by default, %d at most", waitDefault, waitMax)}}
	case taskCancel:
		ids.Required = true
	}
	return []tools.Arg{ids}
}

func (t *bgTool) Schema() map[string]any {
	props := map[string]any{}
	required := []string{}
	for _, a := range t.Args() {
		p := map[string]any{"type": a.Type, "description": a.Desc}
		if a.Name == "ids" {
			p["items"] = map[string]any{"type": "string"}
		}
		props[a.Name] = p
		if a.Required {
			required = append(required, a.Name)
		}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

// Title names the subagents of the call.
func (t *bgTool) Title(args map[string]any) string {
	ids, _ := bgIDs(args)
	return strings.TrimSpace(t.name + " " + strings.Join(ids, ", "))
}

func (t *bgTool) Execute(ctx context.Context, _ tools.Exec, args map[string]any, live io.Writer) (string, error) {
	ids, err := bgIDs(args)
	if err != nil {
		return "", err
	}
	s := t.a.background()
	switch t.name {
	case taskWait:
		res, err := s.wait(ctx, ids, waitTimeout(args), t.a.waitLine(live))
		if err == nil && live != nil {
			io.WriteString(live, res+"\n")
		}
		return res, err
	case taskResult:
		if len(ids) == 0 {
			return s.list(), nil
		}
		if err := s.check(ids); err != nil {
			return "", err
		}
		return s.answers(ids), nil
	}
	return s.cancel(ids)
}

// bgIDs is the ids argument, each once; some models send the array as
// JSON text, or the ids as a list in words.
func bgIDs(args map[string]any) ([]string, error) {
	v := args["ids"]
	if s, ok := v.(string); ok {
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			v = strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
		}
	}
	var ids []string
	switch v := v.(type) {
	case nil:
	case []string:
		ids = v
	case []any:
		for _, x := range v {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("ids: %v is not an id; they are strings, as task gave them (bg1, …)", x)
			}
			ids = append(ids, s)
		}
	default:
		return nil, errors.New("ids: an array of the ids task gave, such as [\"bg1\"], is needed")
	}
	var out []string
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

// waitTimeout is the timeout argument of task_wait, within its bounds.
func waitTimeout(args map[string]any) time.Duration {
	secs := float64(waitDefault)
	switch v := args["timeout"].(type) {
	case float64:
		secs = v
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			secs = f
		}
	}
	secs = min(max(secs, 0), waitMax)
	return time.Duration(secs * float64(time.Second))
}
