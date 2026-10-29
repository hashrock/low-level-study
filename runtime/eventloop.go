package main

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

import (
	"container/heap"
	"context"
	"errors"
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
	fn       goja.Callable
	args     []goja.Value
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

// Loop is the event loop. Create with NewLoop, then call Run on the goroutine
// that owns the goja.Runtime.
type Loop struct {
	vm *goja.Runtime

	timers   timerHeap
	byID     map[int64]*timer
	nextID   int64
	seq      uint64
	inFlight int // async ops started but not yet completed

	completions chan func() error // filled by worker goroutines

	ctx       context.Context // cancelled on timeout / shutdown
	cancel    context.CancelFunc
	interrupt chan struct{} // closed when the wall-clock deadline hits
	once      sync.Once

	// afterTask runs after each macrotask + its microtasks (used for
	// unhandled-rejection detection). May be nil.
	afterTask func() error
}

func NewLoop(vm *goja.Runtime) *Loop {
	ctx, cancel := context.WithCancel(context.Background())
	return &Loop{
		vm:          vm,
		byID:        map[int64]*timer{},
		completions: make(chan func() error, 64),
		ctx:         ctx,
		cancel:      cancel,
		interrupt:   make(chan struct{}),
	}
}

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

func (l *Loop) AddTimer(fn goja.Callable, delay time.Duration, repeat bool, args []goja.Value) int64 {
	if delay < 0 {
		delay = 0
	}
	if repeat && delay < time.Millisecond {
		delay = time.Millisecond // avoid a busy setInterval(fn, 0)
	}
	l.nextID++
	l.seq++
	t := &timer{id: l.nextID, seq: l.seq, when: time.Now().Add(delay),
		interval: delay, repeat: repeat, fn: fn, args: args}
	heap.Push(&l.timers, t)
	l.byID[t.id] = t
	return t.id
}

func (l *Loop) ClearTimer(id int64) {
	if t, ok := l.byID[id]; ok {
		delete(l.byID, id)
		if t.index >= 0 {
			heap.Remove(&l.timers, t.index)
		}
	}
}

// --- async ops ---

// StartOp runs work on a new goroutine. work must not touch goja. When it
// finishes, done(result, err) is called on the loop goroutine.
func (l *Loop) StartOp(work func(ctx context.Context) (interface{}, error), done func(interface{}, error) error) {
	l.inFlight++
	go func() {
		res, err := work(l.ctx)
		select {
		case l.completions <- func() error { return done(res, err) }:
		case <-l.ctx.Done(): // loop is gone; drop the result
		}
	}()
}

// PromiseOp is StartOp + a Promise: resolve with toJS(result) or reject with
// toErr(err). Returns the promise to hand to JS.
func (l *Loop) PromiseOp(work func(ctx context.Context) (interface{}, error),
	toJS func(interface{}) goja.Value, toErr func(error) goja.Value) goja.Value {
	p, resolve, reject := l.vm.NewPromise()
	l.StartOp(work, func(res interface{}, err error) error {
		if err != nil {
			return reject(toErr(err))
		}
		return resolve(toJS(res))
	})
	return l.vm.ToValue(p)
}

// alive: is there anything that could still produce a callback?
func (l *Loop) alive() bool { return len(l.timers) > 0 || l.inFlight > 0 }

func (l *Loop) task(f func() error) error {
	if err := f(); err != nil {
		return err
	}
	if l.afterTask != nil {
		return l.afterTask()
	}
	return nil
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
		if t.repeat {
			l.seq++
			t.seq = l.seq
			t.when = now.Add(t.interval)
			heap.Fix(&l.timers, 0)
		} else {
			heap.Pop(&l.timers)
			delete(l.byID, t.id)
		}
		if err := l.task(func() error { _, err := t.fn(goja.Undefined(), t.args...); return err }); err != nil {
			return err
		}
	}
	return nil
}

// Run executes the main script (as the first macrotask) and then spins the
// loop until there is no more work, an uncaught error occurs, or Stop is
// called.
func (l *Loop) Run(main func() error) (err error) {
	defer l.cancel()
	defer func() {
		// An interrupt surfaces as *goja.InterruptedError from whatever JS
		// was running; normalise it.
		var ie *goja.InterruptedError
		if errors.As(err, &ie) {
			if e, ok := ie.Value().(error); ok {
				err = e
			}
		}
	}()
	if err := l.task(main); err != nil {
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
			case f := <-l.completions:
				l.inFlight--
				if err := l.task(f); err != nil {
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
		case f := <-l.completions:
			l.inFlight--
			err = l.task(f)
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
