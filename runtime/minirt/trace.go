package minirt

// trace.go — the event-loop trace stream.
//
// When Options.Trace is set, the runtime reports what the loop does as a
// stream of Events: tasks, microtasks, timers, async ops, permission checks
// and console output. The browser page draws its call stack / queues /
// timeline purely from this stream.
//
// What is exact and what is approximate (see also the README):
//
//   - Tasks (macrotasks), timers and async ops are ours, so their events are
//     exact.
//   - Microtasks live inside goja's private job queue, which has no hook for
//     "a job was enqueued". We use goja's AsyncContextTracker, which is told
//     when a promise *reaction is registered* (Grab: .then() call or await)
//     and right before/after the reaction's *handler runs* (Resumed/Exited).
//     So "micro.register" is the registration, not the enqueue. Whether the
//     job is already queued is known only when we can see the promise:
//       * queueMicrotask(fn)            -> queued (exact)
//       * p.then(...) on a settled p    -> queued (exact; we patch
//                                          Promise.prototype.then to look at p)
//       * p.then(...) on a pending p    -> "waiting" until p settles; we don't
//                                          see the settle moment, only the run
//       * await x                       -> "unknown": goja's await calls the
//                                          internal then directly, so we never
//                                          see x. It is queued immediately for
//                                          non-promises and settled promises.
//     A reaction without a handler (the fulfil half of .catch(f), etc.) never
//     runs; at task end such records on settled promises are dropped
//     ("micro.drop").
//   - Pass-through jobs that run no user handler (e.g. resolving a promise
//     with a thenable) are invisible.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// Event is one trace entry. Fields are used depending on Type.
type Event struct {
	Seq  int     `json:"seq"`
	T    float64 `json:"t"` // ms since the loop started (virtual time in virtual mode)
	Type string  `json:"type"`

	Task  int64   `json:"task,omitempty"`  // task.*: task number; others: task during which it happened
	ID    int64   `json:"id,omitempty"`    // timer / op / microtask id
	Kind  string  `json:"kind,omitempty"`  // task kind, op kind, perm kind, micro kind
	Label string  `json:"label,omitempty"` // human readable
	Line  int     `json:"line,omitempty"`  // source line (micro.register)
	State string  `json:"state,omitempty"` // micro.register: queued|waiting|unknown
	Delay float64 `json:"delay,omitempty"` // timer.set: requested delay ms
	Due   float64 `json:"due,omitempty"`   // timer.set / op.start: due time ms
	Seq2  uint64  `json:"order,omitempty"` // timer.set: tie-break order in the heap

	Target  string `json:"target,omitempty"` // perm / op: what was asked for
	Path    string `json:"path,omitempty"`   // perm: normalized path
	Allowed bool   `json:"allowed,omitempty"`
	Flag    string `json:"flag,omitempty"`
	Stream  string `json:"stream,omitempty"` // console: out|err
	Text    string `json:"text,omitempty"`
	Err     string `json:"err,omitempty"`
}

// Event types.
const (
	EvTaskStart     = "task.start"     // Kind: main|timer|interval|op
	EvTaskBodyEnd   = "task.body.end"  // the task's own code returned; microtask drain follows
	EvTaskEnd       = "task.end"       // microtasks drained
	EvMicroRegister = "micro.register" // Kind: then|catch|await|queueMicrotask
	EvMicroRun      = "micro.run"
	EvMicroDone     = "micro.done"
	EvMicroQueued   = "micro.queued" // a "waiting" reaction's promise settled (noticed late, see scanWaiting)
	EvMicroDrop     = "micro.drop"
	EvTimerSet      = "timer.set"
	EvTimerFire     = "timer.fire"
	EvTimerClear    = "timer.clear"
	EvOpStart       = "op.start" // Kind: read|write|fetch
	EvOpDone        = "op.done"
	EvPerm          = "perm"
	EvConsole       = "console"
	EvClock         = "clock" // virtual clock advanced to T
	EvExit          = "exit"
)

type tracer struct {
	mu    sync.Mutex
	emit  func(Event)
	seq   int
	start time.Time
	now   func() time.Time
	task  int64 // current task (loop goroutine writes; read under mu)
}

func (t *tracer) Emit(e Event) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	e.Seq = t.seq
	e.T = msSince(t.start, t.now())
	if e.Task == 0 {
		e.Task = t.task
	}
	t.emit(e)
}

func (t *tracer) setTask(id int64) {
	t.mu.Lock()
	t.task = id
	t.mu.Unlock()
}

func msSince(start, now time.Time) float64 {
	return float64(now.Sub(start)) / float64(time.Millisecond)
}

// --- microtask tracking via goja.AsyncContextTracker ---

type microRec struct {
	id      int64
	promise *goja.Promise // nil for await
	state   string
	ran     bool
}

type microTracker struct {
	r       *Runtime
	nextID  int64
	pending *microLabel // set by the patched then / queueMicrotask right before registration
	live    map[int64]*microRec
	waiting []*microRec // registered on a pending promise we can see
	cur     *microRec
}

type microLabel struct {
	kind, label string
	line        int
	state       string
	promise     *goja.Promise
}

func (m *microTracker) Grab() interface{} {
	m.nextID++
	rec := &microRec{id: m.nextID}
	lb := m.pending
	m.pending = nil
	if lb == nil {
		// Not via our patched then: an await (goja calls the internal then).
		// goja reports the function start, not the await, as position, so
		// only the function name is used. Inside a resumed async function
		// the stack is empty; then we name the microtask we are in.
		name, _ := m.r.userFrame()
		label := "await"
		switch {
		case name != "":
			label = "await in " + name
		case m.cur != nil:
			label = fmt.Sprintf("await (continuation of #%d)", m.cur.id)
		}
		lb = &microLabel{kind: "await", label: label, state: "unknown"}
	}
	rec.promise, rec.state = lb.promise, lb.state
	m.live[rec.id] = rec
	if rec.state == "waiting" {
		m.waiting = append(m.waiting, rec)
	}
	m.r.trace.Emit(Event{Type: EvMicroRegister, ID: rec.id, Kind: lb.kind, Label: lb.label, Line: lb.line, State: lb.state})
	return rec
}

func (m *microTracker) Resumed(ctx interface{}) {
	rec, _ := ctx.(*microRec)
	if rec == nil {
		return
	}
	m.scanWaiting()
	rec.ran = true
	m.cur = rec
	m.r.trace.Emit(Event{Type: EvMicroRun, ID: rec.id})
}

func (m *microTracker) Exited() {
	if m.cur == nil {
		return
	}
	delete(m.live, m.cur.id)
	m.r.trace.Emit(Event{Type: EvMicroDone, ID: m.cur.id})
	m.cur = nil
}

// scanWaiting reports reactions whose promise has settled since the last
// look as queued ("micro.queued"). goja enqueues the job at the settle
// moment, which we cannot observe, so this is reported at the next point we
// get control: before the next job runs, at the end of a task body, or at
// the end of the task. The FIFO position is right; the moment is late.
func (m *microTracker) scanWaiting() {
	keep := m.waiting[:0]
	for _, rec := range m.waiting {
		if rec.promise.State() == goja.PromiseStatePending {
			keep = append(keep, rec)
			continue
		}
		if _, live := m.live[rec.id]; live {
			rec.state = "queued"
			m.r.trace.Emit(Event{Type: EvMicroQueued, ID: rec.id})
		}
	}
	m.waiting = keep
}

// endOfTask drops reactions that will never run: registered on a promise
// that is settled by now (so its job, if any, has been drained) but whose
// handler never ran (no handler for that outcome).
func (m *microTracker) endOfTask() {
	ids := make([]int64, 0, len(m.live))
	for id := range m.live {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		rec := m.live[id]
		if rec.promise != nil && rec.promise.State() != goja.PromiseStatePending && !rec.ran {
			delete(m.live, id)
			m.r.trace.Emit(Event{Type: EvMicroDrop, ID: id})
		}
	}
}

// userFrame returns the innermost JS frame that is not our bootstrap code.
func (r *Runtime) userFrame() (string, int) {
	for _, f := range r.vm.CaptureCallStack(8, nil) {
		src := f.SrcName()
		if src == "" || src == bootstrapName || strings.HasPrefix(src, "<native>") {
			continue
		}
		p := f.Position()
		if p.Line == 0 {
			continue
		}
		return f.FuncName(), p.Line
	}
	return "", 0
}

// funcName labels a callback: its name, or for anonymous functions a
// shortened copy of its source ("() => console.log(\"hi\")").
func funcName(v goja.Value) string {
	o, ok := v.(*goja.Object)
	if !ok {
		return ""
	}
	if n := o.Get("name"); n != nil && n.String() != "" {
		return n.String()
	}
	src := strings.Join(strings.Fields(o.String()), " ")
	if strings.Contains(src, "[native code]") {
		return "native"
	}
	if src == "" {
		return "anonymous"
	}
	if r := []rune(src); len(r) > 48 {
		src = string(r[:47]) + "…"
	}
	return src
}

// installTraceHooks patches Promise.prototype.then (to see the promise and
// handler of each registration) and defines a traced queueMicrotask.
func (r *Runtime) installTraceHooks(hooks *goja.Object) {
	mt := &microTracker{r: r, live: map[int64]*microRec{}}
	r.micro = mt
	r.vm.SetAsyncContextTracker(mt)
	_ = hooks.Set("beforeThen", func(c goja.FunctionCall) goja.Value {
		lb := &microLabel{kind: "then", state: "unknown"}
		onF, onR := c.Argument(1), c.Argument(2)
		_, fOK := goja.AssertFunction(onF)
		_, rOK := goja.AssertFunction(onR)
		switch {
		case fOK:
			lb.label = "then(" + funcName(onF) + ")"
		case rOK:
			lb.kind, lb.label = "catch", "catch("+funcName(onR)+")"
		default:
			lb.label = "then()"
		}
		if o, ok := c.Argument(0).(*goja.Object); ok {
			if p, ok := o.Export().(*goja.Promise); ok {
				lb.promise = p
				if p.State() == goja.PromiseStatePending {
					lb.state = "waiting"
				} else {
					lb.state = "queued"
				}
			}
		}
		_, lb.line = r.userFrame()
		mt.pending = lb
		return goja.Undefined()
	})
	_ = hooks.Set("afterThen", func() { mt.pending = nil })
	_ = hooks.Set("beforeQueueMicrotask", func(fn goja.Value) {
		_, line := r.userFrame()
		mt.pending = &microLabel{kind: "queueMicrotask", label: "queueMicrotask(" + funcName(fn) + ")", line: line, state: "queued"}
	})
	_ = hooks.Set("bodyEnd", func() { r.bodyEnd() })
}

// traceBootstrapJS runs only when tracing is on. It receives the hooks object
// as an argument, so nothing is left on globalThis for scripts to call.
const traceBootstrapJS = `
(function (g, hooks) {
  const origThen = Promise.prototype.then;
  Object.defineProperty(Promise.prototype, "then", {
    value: function then(onFulfilled, onRejected) {
      hooks.beforeThen(this, onFulfilled, onRejected);
      try { return origThen.call(this, onFulfilled, onRejected); } finally { hooks.afterThen(); }
    },
    writable: true, configurable: true,
  });
  const resolved = Promise.resolve();
  g.queueMicrotask = function queueMicrotask(fn) {
    if (typeof fn !== "function") throw new TypeError("queueMicrotask: callback must be a function");
    hooks.beforeQueueMicrotask(fn);
    origThen.call(resolved, () => { fn(); });
  };
  // callTask runs a timer callback and marks where its own code ends; the
  // microtask drain happens after this returns to Go.
  return function callTask(fn, args) {
    try { return fn(...args); } finally { hooks.bodyEnd(); }
  };
})
`

// bodyEnd marks the end of the current task's own code.
func (r *Runtime) bodyEnd() {
	r.trace.Emit(Event{Type: EvTaskBodyEnd})
	if r.micro != nil {
		r.micro.scanWaiting()
	}
}
