package connector

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

type atomicRunner struct {
	fake
	active  atomic.Int32
	started atomic.Int32
	ended   atomic.Int32
	onStart chan struct{}
	onEnd   chan struct{}
}

func (r *atomicRunner) Run(ctx context.Context) {
	r.active.Add(1)
	r.started.Add(1)
	if r.onStart != nil {
		select {
		case r.onStart <- struct{}{}:
		default:
		}
	}
	<-ctx.Done()
	r.active.Add(-1)
	r.ended.Add(1)
	if r.onEnd != nil {
		select {
		case r.onEnd <- struct{}{}:
		default:
		}
	}
}

type minimalRunner struct {
	id      string
	active  atomic.Int32
	onStart chan struct{}
	onEnd   chan struct{}
}

func (m *minimalRunner) ID() string         { return m.id }
func (m *minimalRunner) Name() string       { return "Minimal" }
func (m *minimalRunner) Manifest() Manifest { return Manifest{} }
func (m *minimalRunner) Run(ctx context.Context) {
	m.active.Add(1)
	if m.onStart != nil {
		select {
		case m.onStart <- struct{}{}:
		default:
		}
	}
	<-ctx.Done()
	m.active.Add(-1)
	if m.onEnd != nil {
		select {
		case m.onEnd <- struct{}{}:
		default:
		}
	}
}

// dynamicUncomparableConnector has a statically comparable struct type (all interface fields),
// so reflect.Type.Comparable() reports true, while reflect.Value.Comparable() reports false
// whenever meta holds an uncomparable dynamic value such as a slice.
type dynamicUncomparableConnector struct {
	Connector
	meta any
}

func (d dynamicUncomparableConnector) Run(ctx context.Context) {
	if rn, ok := d.Connector.(Runner); ok {
		rn.Run(ctx)
	}
}

func TestStartOneDoesNotStartRemovedConnector(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	noopWatch := func(ctx context.Context, _ func(Scope)) error {
		<-ctx.Done()
		return nil
	}

	r := NewRegistry(nil)
	c1 := &runs{
		fake:    fake{id: "example", status: Status{State: StateOK}, watch: noopWatch},
		started: make(chan struct{}, 4),
		ended:   make(chan struct{}, 4),
	}
	if err := r.Add(c1); err != nil {
		t.Fatal(err)
	}
	r.Start(ctx, func(string, Scope) {}, t.Logf)
	defer r.Stop()
	waitOn(t, c1.started, "c1 start")

	r.Remove("example")
	waitOn(t, c1.ended, "c1 stop on remove")

	// Simulate an Add / SetEnabled / Start call invoking startOne for c1 after Remove("example") ran.
	r.startOne(c1)

	select {
	case <-c1.started:
		t.Fatal("startOne restarted a removed connector")
	default:
	}
	r.mu.RLock()
	leakedStop := r.stop["example"] != nil
	r.mu.RUnlock()
	if leakedStop {
		t.Fatal("startOne leaked r.stop entry for a removed connector")
	}

	// Re-adding a replacement connector with the same ID must start cleanly.
	c2 := &runs{
		fake:    fake{id: "example", status: Status{State: StateOK}, watch: noopWatch},
		started: make(chan struct{}, 4),
		ended:   make(chan struct{}, 4),
	}
	if err := r.Add(c2); err != nil {
		t.Fatal(err)
	}
	waitOn(t, c2.started, "c2 start after c1 removed")
}

func TestStartOffAndReAddKeepsStatusDisabled(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry(nil)
	defer r.Remove("example")
	r.StartOff("example")

	c1 := &fake{id: "example", status: Status{State: StateOK}}
	if err := r.Add(c1); err != nil {
		t.Fatal(err)
	}
	if st := StatusOf(ctx, c1); st.State != StateDisabled {
		t.Fatalf("StatusOf after StartOff+Add = %+v, want %q", st, StateDisabled)
	}

	r.Remove("example")
	if st := StatusOf(ctx, c1); st.State != StateOK {
		t.Fatalf("StatusOf removed connector = %+v, want %q", st, StateOK)
	}

	// Remove preserves the disabled switch; a re-added instance must also report StateDisabled.
	c2 := &fake{id: "example", status: Status{State: StateOK}}
	if err := r.Add(c2); err != nil {
		t.Fatal(err)
	}
	if st := StatusOf(ctx, c2); st.State != StateDisabled {
		t.Fatalf("StatusOf after Remove+Add while disabled = %+v, want %q", st, StateDisabled)
	}
}

func TestStartOffStopsRunningConnector(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	noopWatch := func(ctx context.Context, _ func(Scope)) error {
		<-ctx.Done()
		return nil
	}

	r := NewRegistry(nil)
	defer r.Remove("example")
	c := &runs{
		fake:    fake{id: "example", status: Status{State: StateOK}, watch: noopWatch},
		started: make(chan struct{}, 2),
		ended:   make(chan struct{}, 2),
	}
	if err := r.Add(c); err != nil {
		t.Fatal(err)
	}
	r.Start(ctx, func(string, Scope) {}, t.Logf)
	defer r.Stop()
	waitOn(t, c.started, "initial connector start")

	r.StartOff("example")
	waitOn(t, c.ended, "connector stop on StartOff")

	if r.Enabled("example") {
		t.Fatal("Enabled returned true after StartOff")
	}
	if st := StatusOf(ctx, c); st.State != StateDisabled {
		t.Fatalf("StatusOf after StartOff = %+v, want %q", st, StateDisabled)
	}
	r.mu.RLock()
	leakedStop := r.stop["example"] != nil
	r.mu.RUnlock()
	if leakedStop {
		t.Fatal("StartOff left r.stop entry behind")
	}
}

func TestDynamicUncomparableValueConnectorDoesNotPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	base := &minimalRunner{
		id:      "example",
		onStart: make(chan struct{}, 2),
		onEnd:   make(chan struct{}, 2),
	}
	uncomp := dynamicUncomparableConnector{
		Connector: base,
		meta:      []string{"uncomparable"},
	}
	if !reflect.TypeOf(uncomp).Comparable() {
		t.Fatal("test fixture expected statically comparable struct type")
	}
	if reflect.ValueOf(uncomp).Comparable() {
		t.Fatal("test fixture expected dynamically uncomparable value")
	}

	r := NewRegistry(nil)
	defer r.Remove("example")
	if err := r.Add(uncomp); err != nil {
		t.Fatal(err)
	}
	r.Start(ctx, func(string, Scope) {}, t.Logf)
	defer r.Stop()
	waitOn(t, base.onStart, "uncomparable connector start")

	if st := StatusOf(ctx, uncomp); st.State != StateOK {
		t.Fatalf("StatusOf = %+v, want %q", st, StateOK)
	}
	if err := r.SetEnabled("example", false); err == nil {
		t.Fatal("SetEnabled accepted dynamically uncomparable connector")
	}

	// StartOff and Remove must not panic and must cancel the running worker.
	r.StartOff("example")
	waitOn(t, base.onEnd, "uncomparable connector stop on StartOff")
	r.Remove("example")

	// Re-adding while off must not panic or start.
	if err := r.Add(uncomp); err != nil {
		t.Fatal(err)
	}
	if base.active.Load() != 0 {
		t.Fatalf("re-added disabled connector started: active=%d", base.active.Load())
	}
	r.Remove("example")

	// A stale dynamically uncomparable value passed to startOne against a dynamically comparable
	// replacement of the same concrete type must not panic or start the stale instance.
	base2 := &minimalRunner{
		id:      "example",
		onStart: make(chan struct{}, 2),
		onEnd:   make(chan struct{}, 2),
	}
	comp := dynamicUncomparableConnector{
		Connector: base2,
		meta:      "comparable",
	}
	if !reflect.ValueOf(comp).Comparable() {
		t.Fatal("expected comparable replacement value")
	}
	r2 := NewRegistry(nil)
	defer r2.Remove("example")
	if err := r2.Add(comp); err != nil {
		t.Fatal(err)
	}
	r2.mu.Lock()
	r2.run = &starter{ctx: ctx, changed: func(string, Scope) {}, logf: t.Logf}
	r2.mu.Unlock()
	r2.startOne(uncomp)
	if base.active.Load() != 0 || base2.active.Load() != 0 {
		t.Fatalf("stale uncomparable startOne started a runner: base=%d base2=%d", base.active.Load(), base2.active.Load())
	}
	r2.Stop()
}

func TestConcurrentSetEnabledAddRemoveNoOrphanedRunners(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := NewRegistry(nil)
	r.Start(ctx, func(string, Scope) {}, t.Logf)

	var mu sync.Mutex
	var created []*atomicRunner

	noopWatch := func(ctx context.Context, _ func(Scope)) error {
		<-ctx.Done()
		return nil
	}
	newRunner := func(onStart chan struct{}) *atomicRunner {
		c := &atomicRunner{
			fake:    fake{id: "example", status: Status{State: StateOK}, watch: noopWatch},
			onStart: onStart,
		}
		mu.Lock()
		created = append(created, c)
		mu.Unlock()
		return c
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for range 100 {
			c := newRunner(nil)
			_ = r.Add(c)
			r.Remove("example")
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := range 100 {
			_ = r.SetEnabled("example", i%2 == 0)
		}
	}()
	close(start)
	wg.Wait()

	r.Remove("example")
	r.Stop()

	for i, c := range created {
		if active := c.active.Load(); active != 0 {
			t.Fatalf("connector #%d still has %d active runner(s) after Stop", i, active)
		}
		if started, ended := c.started.Load(), c.ended.Load(); started != ended {
			t.Fatalf("connector #%d mismatched Run count: started=%d ended=%d", i, started, ended)
		}
		if _, leaked := switchedOff.Load(c); leaked {
			t.Fatalf("removed connector #%d leaked in switchedOff", i)
		}
	}

	// After cleanup, a fresh Start + Add + SetEnabled(true) must start cleanly.
	r.Start(ctx, func(string, Scope) {}, t.Logf)
	defer r.Stop()
	defer r.Remove("example")
	finalStarted := make(chan struct{}, 1)
	final := newRunner(finalStarted)
	if err := r.Add(final); err != nil {
		t.Fatal(err)
	}
	if err := r.SetEnabled("example", true); err != nil {
		t.Fatal(err)
	}
	waitOn(t, finalStarted, "final connector start")
}
