package minirt

// eventloop.go — a single-threaded event loop around goja.
//
// Golden rule: *only the loop goroutine touches goja.Runtime*. goja is not
// goroutine-safe. Background goroutines (file I/O, HTTP) never see JS values;
// they compute plain Go results and send a closure back through `completions`.
// The loop goroutine runs that closure, which converts the result to JS and
// resolves the Promise.
//
// Microtasks: goja keeps its own Promise job queue and drains it whenever
// control returns from JS to Go at the outermost level (Runtime.leave()).
// Every macrotask we run (main script, timer callback, op completion) is such
// an outermost call, so "run one macrotask, then drain all microtasks" falls
// out naturally — exactly the HTML/Node ordering.
//
// Two clocks:
//   - real (default): timers use time.Now, ops run on goroutines, Run blocks
//     until there is no more work.
//   - virtual (Options.VirtualClock): time only moves when the loop has
//     nothing due and jumps straight to the next deadline. Op work runs
//     inline when started and its result is delivered at start+latency of
//     virtual time. Step() runs exactly one macrotask (+ its microtasks), so
//     a UI can single-step the loop deterministically.

import (
	"container/heap"
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/dop251/goja"
)

var ErrTimeout = errors.New("execution timed out")

type timer struct {
	id       int64
	seq      uint64 // tie-breaker: FIFO for equal deadlines
	when     time.Time
	interval time.Duration // 0 for setTimeout
	repeat   bool
	fn       goja.Value
	args     []goja.Value
	label    string
	index    int // heap index
}

type timerHeap []*timer

func (h timerHeap) Len() int { return len(h) }
func (h timerHeap) Less(i, j int) bool {
	if h[i].when.Equal(h[j].when) {
		return h[i].seq < h[j].seq
	}
	return h[i].when.Before(h[j].when)
}
func (h timerHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *timerHeap) Push(x interface{}) { t := x.(*timer); t.index = len(*h); *h = append(*h, t) }
func (h *timerHeap) Pop() interface{} {
	old := *h
	t := old[len(old)-1]
	*h = old[:len(old)-1]
	t.index = -1
	return t
}

// Op describes one async host operation (read, write, fetch).
type Op struct {
	ID     int64
	Kind   string // "read", "write", "fetch"
	Target string
	// Latency may be set by the work function: the simulated duration of
	// the op (FakeNetwork / MemFS). The loop delays delivery by it.
	Latency time.Duration

	started time.Time
	readyAt time.Time // virtual mode
	seq     uint64
	done    func() error
	err     error // result error (for the trace)
	index   int
}

type opHeap []*Op

func (h opHeap) Len() int { return len(h) }
func (h opHeap) Less(i, j int) bool {
	if h[i].readyAt.Equal(h[j].readyAt) {
		return h[i].seq < h[j].seq
	}
	return h[i].readyAt.Before(h[j].readyAt)
}
func (h opHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *opHeap) Push(x interface{}) { o := x.(*Op); o.index = len(*h); *h = append(*h, o) }
func (h *opHeap) Pop() interface{} {
	old := *h
	o := old[len(old)-1]
	*h = old[:len(old)-1]
	return o
}

// Loop is the event loop. Create with NewLoop, then call Run (or Start +
// Step in virtual mode) on the goroutine that owns the goja.Runtime.
type Loop struct {
	vm *goja.Runtime

	timers   timerHeap
	byID     map[int64]*timer
	nextID   int64
	seq      uint64
	inFlight int // async ops started but not yet completed
	ops      map[int64]*Op
	nextOpID int64
	nextTask int64

	completions chan *Op // real mode: filled by worker goroutines

	ctx       context.Context // cancelled on timeout / shutdown
	cancel    context.CancelFunc
	interrupt chan struct{} // closed when the wall-clock deadline hits
	once      sync.Once

	// afterTask runs after each macrotask + its microtasks (used for
	// unhandled-rejection detection). May be nil.
	afterTask func() error

	// virtual clock
	virtual  bool
	start    time.Time
	vnow     time.Time
	vops     opHeap // completed (inline) ops waiting for their virtual readyAt
	vtimeout time.Duration

	// stepping state (virtual mode)
	main     func() error
	mainDone bool
	finished bool
	finalErr error

	trace    *tracer
	callTask goja.Callable // traced mode: JS helper that marks the end of a task body
}

func NewLoop(vm *goja.Runtime) *Loop {
	ctx, cancel := context.WithCancel(context.Background())
	return &Loop{
		vm:          vm,
		byID:        map[int64]*timer{},
		ops:         map[int64]*Op{},
		completions: make(chan *Op, 64),
		ctx:         ctx,
		cancel:      cancel,
		interrupt:   make(chan struct{}),
		start:       time.Now(),
	}
}

// virtualEpoch is where the virtual clock starts (Date.now() reports it).
var virtualEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// UseVirtualClock switches the loop to virtual time. Call before running.
// timeout (if > 0) is a limit on *virtual* time.
func (l *Loop) UseVirtualClock(timeout time.Duration) {
	l.virtual = true
	l.start = virtualEpoch
	l.vnow = virtualEpoch
	l.vtimeout = timeout
}

// Now is the loop's clock (virtual or real).
func (l *Loop) Now() time.Time {
	if l.virtual {
		return l.vnow
	}
	return time.Now()
}

func (l *Loop) ms(t time.Time) float64 { return msSince(l.start, t) }

// Stop may be called from any goroutine (e.g. the timeout timer). It
// interrupts JS currently executing and wakes the loop if it is idle.
func (l *Loop) Stop(reason error) {
	l.once.Do(func() {
		l.vm.Interrupt(reason)
		l.cancel()
		close(l.interrupt)
	})
}

// --- timers (loop goroutine only) ---

func (l *Loop) AddTimer(fn goja.Value, delay time.Duration, repeat bool, args []goja.Value, label string) int64 {
	if delay < 0 {
		delay = 0
	}
	if repeat && delay < time.Millisecond {
		delay = time.Millisecond // avoid a busy setInterval(fn, 0)
	}
	l.nextID++
	l.seq++
	t := &timer{id: l.nextID, seq: l.seq, when: l.Now().Add(delay),
		interval: delay, repeat: repeat, fn: fn, args: args, label: label}
	heap.Push(&l.timers, t)
	l.byID[t.id] = t
	l.traceTimerSet(t, EvTimerSet)
	return t.id
}

func (l *Loop) traceTimerSet(t *timer, typ string) {
	if l.trace == nil {
		return
	}
	kind := "timeout"
	if t.repeat {
		kind = "interval"
	}
	l.trace.Emit(Event{Type: typ, ID: t.id, Kind: kind, Label: t.label,
		Delay: float64(t.interval) / float64(time.Millisecond), Due: l.ms(t.when), Seq2: t.seq})
}

func (l *Loop) ClearTimer(id int64) {
	if t, ok := l.byID[id]; ok {
		delete(l.byID, id)
		if t.index >= 0 {
			heap.Remove(&l.timers, t.index)
		}
		if l.trace != nil {
			l.trace.Emit(Event{Type: EvTimerClear, ID: id})
		}
	}
}

// --- async ops ---

// StartOp runs work off the JS thread (a goroutine in real mode, inline in
// virtual mode). work must not touch goja. When it finishes, done(result,
// err) is called on the loop goroutine as a new macrotask.
func (l *Loop) StartOp(op *Op, work func(ctx context.Context, op *Op) (interface{}, error), done func(interface{}, error) error) {
	l.inFlight++
	l.nextOpID++
	op.ID = l.nextOpID
	op.started = l.Now()
	l.ops[op.ID] = op
	finish := func(res interface{}, err error) {
		op.err = err
		op.done = func() error { return done(res, err) }
	}
	if l.virtual {
		res, err := work(l.ctx, op)
		finish(res, err)
		l.seq++
		op.seq = l.seq
		op.readyAt = op.started.Add(op.Latency)
		heap.Push(&l.vops, op)
		l.traceOpStart(op, op.readyAt)
		return
	}
	l.traceOpStart(op, time.Time{})
	go func() {
		res, err := work(l.ctx, op)
		if op.Latency > 0 { // simulated latency (fake network) in real time
			t := time.NewTimer(op.Latency)
			select {
			case <-t.C:
			case <-l.ctx.Done():
				t.Stop()
				return
			}
		}
		finish(res, err)
		select {
		case l.completions <- op:
		case <-l.ctx.Done(): // loop is gone; drop the result
		}
	}()
}

func (l *Loop) traceOpStart(op *Op, due time.Time) {
	if l.trace == nil {
		return
	}
	e := Event{Type: EvOpStart, ID: op.ID, Kind: op.Kind, Target: op.Target}
	if !due.IsZero() {
		e.Due = l.ms(due)
	}
	l.trace.Emit(e)
}

// PromiseOp is StartOp + a Promise: resolve with toJS(result) or reject with
// toErr(err). Returns the promise to hand to JS.
func (l *Loop) PromiseOp(op *Op, work func(ctx context.Context, op *Op) (interface{}, error),
	toJS func(interface{}) goja.Value, toErr func(error) goja.Value) goja.Value {
	p, resolve, reject := l.vm.NewPromise()
	l.StartOp(op, work, func(res interface{}, err error) error {
		if err != nil {
			return reject(toErr(err))
		}
		return resolve(toJS(res))
	})
	return l.vm.ToValue(p)
}

// alive: is there anything that could still produce a callback?
func (l *Loop) alive() bool { return len(l.timers) > 0 || l.inFlight > 0 }

func (l *Loop) task(kind string, id int64, label string, f func() error) (err error) {
	l.nextTask++
	if l.trace != nil {
		l.trace.setTask(l.nextTask)
		l.trace.Emit(Event{Type: EvTaskStart, Kind: kind, ID: id, Label: label})
		defer func() {
			e := Event{Type: EvTaskEnd, Kind: kind, ID: id}
			if err != nil {
				e.Err = FormatError(normalizeErr(err))
			}
			l.trace.Emit(e)
			l.trace.setTask(0)
		}()
	}
	if err := f(); err != nil {
		return err
	}
	if l.afterTask != nil {
		return l.afterTask()
	}
	return nil
}

func (l *Loop) runTimer(t *timer) error {
	kind := "timer"
	if t.repeat {
		kind = "interval"
	}
	return l.task(kind, t.id, t.label, func() error {
		if l.trace != nil {
			l.trace.Emit(Event{Type: EvTimerFire, ID: t.id})
		}
		if l.callTask != nil {
			_, err := l.callTask(goja.Undefined(), t.fn, l.vm.NewArray(valuesToIfaces(t.args)...))
			return err
		}
		fn, _ := goja.AssertFunction(t.fn)
		_, err := fn(goja.Undefined(), t.args...)
		return err
	})
}

func valuesToIfaces(vs []goja.Value) []interface{} {
	out := make([]interface{}, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}

// popTimer removes (or re-arms, for intervals) the timer at the top of the heap.
func (l *Loop) popTimer(now time.Time) *timer {
	t := l.timers[0]
	if t.repeat {
		l.seq++
		t.seq = l.seq
		t.when = now.Add(t.interval)
		heap.Fix(&l.timers, 0)
		l.traceTimerSet(t, EvTimerSet)
	} else {
		heap.Pop(&l.timers)
		delete(l.byID, t.id)
	}
	return t
}

func (l *Loop) runOp(op *Op) error {
	l.inFlight--
	delete(l.ops, op.ID)
	return l.task("op", op.ID, op.Kind+" "+op.Target, func() error {
		if l.trace != nil {
			e := Event{Type: EvOpDone, ID: op.ID, Kind: op.Kind, Target: op.Target}
			if op.err != nil {
				e.Err = op.err.Error()
			}
			l.trace.Emit(e)
		}
		// The task body is just "resolve/reject the promise"; the awaiting
		// code runs as microtasks right after (goja drains them inside
		// done(), so the body end is marked first).
		if l.trace != nil {
			l.trace.Emit(Event{Type: EvTaskBodyEnd})
		}
		return op.done()
	})
}

// runDueTimers fires every timer whose deadline has passed and that existed
// when this pass started (timers added during the pass wait for the next one,
// so a setTimeout(f, 0) chain cannot starve I/O completions).
func (l *Loop) runDueTimers() error {
	now := time.Now()
	limit := l.seq
	for len(l.timers) > 0 {
		t := l.timers[0]
		if t.when.After(now) || t.seq > limit {
			return nil
		}
		if err := l.runTimer(l.popTimer(now)); err != nil {
			return err
		}
	}
	return nil
}

func normalizeErr(err error) error {
	// An interrupt surfaces as *goja.InterruptedError from whatever JS
	// was running; normalise it.
	var ie *goja.InterruptedError
	if errors.As(err, &ie) {
		if e, ok := ie.Value().(error); ok {
			return e
		}
	}
	return err
}

// Run executes the main script (as the first macrotask) and then spins the
// loop until there is no more work, an uncaught error occurs, or Stop is
// called.
func (l *Loop) Run(main func() error) (err error) {
	if l.virtual {
		l.Start(main)
		for {
			done, err := l.Step()
			if done {
				return err
			}
		}
	}
	defer l.cancel()
	defer func() { err = normalizeErr(err) }()
	if err := l.task("main", 0, "main script", main); err != nil {
		return err
	}
	for {
		select {
		case <-l.interrupt:
			return ErrTimeout
		default:
		}
		if err := l.runDueTimers(); err != nil {
			return err
		}
		// Drain completions that are already waiting, without blocking.
	drain:
		for {
			select {
			case op := <-l.completions:
				if err := l.runOp(op); err != nil {
					return err
				}
			default:
				break drain
			}
		}
		if !l.alive() {
			return nil
		}
		// Sleep until the next timer, a completion, or the deadline.
		var wake <-chan time.Time
		var tm *time.Timer
		if len(l.timers) > 0 {
			tm = time.NewTimer(time.Until(l.timers[0].when))
			wake = tm.C
		}
		select {
		case op := <-l.completions:
			err = l.runOp(op)
		case <-wake:
		case <-l.interrupt:
			err = ErrTimeout
		}
		if tm != nil {
			tm.Stop()
		}
		if err != nil {
			return err
		}
	}
}

// --- virtual mode stepping ---

// Start prepares a virtual-clock run; the main script is the first Step.
func (l *Loop) Start(main func() error) { l.main = main }

// Finished reports whether the run is over (and its error).
func (l *Loop) Finished() (bool, error) { return l.finished, l.finalErr }

// Step runs exactly one macrotask and the microtasks it queues. If nothing is
// due, the virtual clock first jumps to the next timer / op deadline.
// done=true means the run is over (err is its result).
func (l *Loop) Step() (done bool, err error) {
	if l.finished {
		return true, l.finalErr
	}
	err = normalizeErr(l.step1())
	if err != nil || (l.mainDone && !l.alive()) {
		l.finished, l.finalErr = true, err
		l.cancel()
		return true, err
	}
	return false, nil
}

func (l *Loop) step1() error {
	select {
	case <-l.interrupt:
		return ErrTimeout
	default:
	}
	if !l.mainDone {
		l.mainDone = true
		return l.task("main", 0, "main script", l.main)
	}
	var t *timer
	var op *Op
	if len(l.timers) > 0 {
		t = l.timers[0]
	}
	if len(l.vops) > 0 {
		op = l.vops[0]
	}
	if t == nil && op == nil {
		return nil
	}
	useOp := op != nil && (t == nil || op.readyAt.Before(t.when) ||
		(op.readyAt.Equal(t.when) && op.seq < t.seq))
	var due time.Time
	if useOp {
		due = op.readyAt
	} else {
		due = t.when
	}
	if due.After(l.vnow) {
		if l.vtimeout > 0 && due.Sub(l.start) > l.vtimeout {
			l.vnow = l.start.Add(l.vtimeout)
			return ErrTimeout
		}
		l.vnow = due
		if l.trace != nil {
			l.trace.Emit(Event{Type: EvClock})
		}
	}
	if useOp {
		heap.Pop(&l.vops)
		return l.runOp(op)
	}
	return l.runTimer(l.popTimer(l.vnow))
}

// --- snapshot ---

type TimerInfo struct {
	ID     int64   `json:"id"`
	Due    float64 `json:"due"`
	Delay  float64 `json:"delay"`
	Repeat bool    `json:"repeat"`
	Label  string  `json:"label"`
	Order  uint64  `json:"order"`
}

type OpInfo struct {
	ID      int64   `json:"id"`
	Kind    string  `json:"kind"`
	Target  string  `json:"target"`
	Started float64 `json:"started"`
	Due     float64 `json:"due,omitempty"`
}

type LoopSnapshot struct {
	Now      float64     `json:"now"`
	Tasks    int64       `json:"tasks"`
	Timers   []TimerInfo `json:"timers"` // sorted by (due, order)
	Ops      []OpInfo    `json:"ops"`
	Finished bool        `json:"finished"`
	Error    string      `json:"error,omitempty"`
}

// Snapshot describes the loop state (loop goroutine only).
func (l *Loop) Snapshot() LoopSnapshot {
	s := LoopSnapshot{Now: l.ms(l.Now()), Tasks: l.nextTask, Finished: l.finished,
		Timers: []TimerInfo{}, Ops: []OpInfo{}}
	if l.finalErr != nil {
		s.Error = FormatError(l.finalErr)
	}
	ts := append(timerHeap(nil), l.timers...)
	sortTimers(ts)
	for _, t := range ts {
		s.Timers = append(s.Timers, TimerInfo{ID: t.id, Due: l.ms(t.when), Delay: float64(t.interval) / float64(time.Millisecond),
			Repeat: t.repeat, Label: t.label, Order: t.seq})
	}
	for _, op := range l.ops {
		oi := OpInfo{ID: op.ID, Kind: op.Kind, Target: op.Target, Started: l.ms(op.started)}
		if l.virtual {
			oi.Due = l.ms(op.readyAt)
		}
		s.Ops = append(s.Ops, oi)
	}
	sortOps(s.Ops)
	return s
}

func sortTimers(ts timerHeap) { sort.Slice(ts, func(i, j int) bool { return ts.Less(i, j) }) }
func sortOps(os []OpInfo)     { sort.Slice(os, func(i, j int) bool { return os[i].ID < os[j].ID }) }
