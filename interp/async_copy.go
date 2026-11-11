package interp

import (
	"context"
	"io"
	"runtime"
	"sync"
)

// asyncCopier copies one Lisp object graph into dst without invoking the VM or
// GC. Publish each shell before following its edges to preserve cycles and
// aliases, including multiple boxes referring to the same Go-side Lisp object.
// The source must be exclusively owned by the caller, or a completed task.
// Keep its Interp alive throughout copying: Values contain integer pointers
// that the Go collector cannot use to retain the source heap.
type asyncCopier struct {
	dst       *Interp
	ctx       context.Context
	values    map[Value]Value
	functions map[*Function]*Function
	cells     map[*FrameCell]*FrameCell
	tables    map[*Table]*Table
	modules   map[*Module]*Module
	tasks     map[*asyncTask]*asyncTask
	source    *Interp // non-nil only when compacting a worker's result
	ambient   *Module
	budget    *taskScope // only snapshots and completion graphs charge the scope
}

func newAsyncCopier(dst *Interp, ctx context.Context, budget ...*taskScope) *asyncCopier {
	c := &asyncCopier{
		dst: dst, ctx: ctx,
		values:    make(map[Value]Value),
		functions: make(map[*Function]*Function),
		cells:     make(map[*FrameCell]*FrameCell),
		tables:    make(map[*Table]*Table),
		modules:   make(map[*Module]*Module),
		tasks:     make(map[*asyncTask]*asyncTask),
	}
	if len(budget) > 0 {
		c.budget = budget[0]
	}
	return c
}

// newAsyncResultCopier retains source globals only if the result graph reaches
// a function needing them. Snapshot and wait import copies use newAsyncCopier:
// ordinary nil environments must still resolve in the executing child.
func newAsyncResultCopier(dst *Interp, ctx context.Context, source *Interp, budget ...*taskScope) *asyncCopier {
	c := newAsyncCopier(dst, ctx, budget...)
	c.source = source
	return c
}

func (c *asyncCopier) ambientModule() *Module {
	if c.ambient == nil {
		// Publish before copying: globals can contain functions pointing back
		// to this namespace, including the function currently being copied.
		c.ambient = &Module{sealed: true}
		c.ambient.globals = c.valuesSlice(c.source.globals)
	}
	return c.ambient
}

func (c *asyncCopier) checkContext() {
	if err := c.ctx.Err(); err != nil {
		panic(err)
	}
}

// Shared output writers may be host buffers rather than concurrency-safe files.
// Install the wrapper on the parent before publishing the first child.
type taskWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *taskWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}

// snapshotAsync runs synchronously on the spawning interpreter's owner.
// Go callbacks and objects remain shared host capabilities and must not
// capture or inject another interpreter's mutable state or Lisp heap values.
func (g *Interp) snapshotAsync(ctx context.Context, thunk Value) (*Interp, Value) {
	defer runtime.KeepAlive(g)
	if _, ok := g.stdout.(*taskWriter); !ok {
		g.stdout = &taskWriter{writer: g.stdout}
	}
	child := &Interp{
		heap:               newHeap(),
		stack:              make([]Value, initialStackSize),
		stdout:             g.stdout,
		macroExpandEnabled: g.macroExpandEnabled,
		builtins:           append([]*Builtin(nil), g.builtins...),
		constants:          append([]bool(nil), g.constants...),
		modules:            make(map[string]*Module, len(g.modules)),
		fileStack:          append([]string(nil), g.fileStack...),
	}
	child.heap.gensymN = g.heap.gensymN
	if g.reader != nil {
		reader := *g.reader // strings.Reader's immutable string may be shared.
		child.reader = &reader
	}
	c := newAsyncCopier(child, ctx, g.taskScope)
	child.globals = c.valuesSlice(g.globals)
	child.macros = c.valuesSlice(g.macros)
	for path, m := range g.modules {
		child.modules[path] = c.module(m)
	}
	child.modStack = make([]*Module, len(g.modStack))
	for i, m := range g.modStack {
		child.modStack[i] = c.module(m)
	}
	copiedThunk := c.value(thunk)
	return child, copiedThunk
}

func (c *asyncCopier) valuesSlice(src []Value) []Value {
	if src == nil {
		return nil
	}
	dst := make([]Value, len(src))
	for i, v := range src {
		dst[i] = c.value(v)
	}
	return dst
}

func (c *asyncCopier) value(v Value) Value {
	c.checkContext()
	if !IsHeap(v) {
		return v
	}
	if dst, ok := c.values[v]; ok {
		return dst
	}
	g := c.dst
	c.charge(v)
	switch heapType(v) {
	case tCons:
		dst := g.Cons(Nil, Nil)
		c.values[v] = dst
		root := dst
		for {
			SetCar(dst, c.value(carFast(v)))
			v = cdrFast(v)
			if !IsCons(v) {
				SetCdr(dst, c.value(v))
				return root
			}
			c.checkContext()
			if tail, ok := c.values[v]; ok {
				SetCdr(dst, tail)
				return root
			}
			// Publish each tail shell before traversing its car. This keeps
			// aliases and cycles intact without a Go frame per list element.
			c.charge(v)
			tail := g.Cons(Nil, Nil)
			c.values[v] = tail
			SetCdr(dst, tail)
			dst = tail
		}
	case tVector:
		src := vectorData(v)
		dst := g.NewVector(len(src), Nil)
		c.values[v] = dst
		data := vectorData(dst)
		for i, e := range src {
			data[i] = c.value(e)
		}
		return dst
	case tString:
		dst := g.NewString(GoString(v))
		c.values[v] = dst
		return dst
	case tBoxed:
		dst := g.box(nil)
		c.values[v] = dst
		boxOf(dst).data = c.boxData(boxOf(v).data)
		return dst
	default:
		raise(symTypeError, "par/wait: cannot copy heap object of type %s", heapType(v))
		return Nil
	}
}

// Copied objects retain their cell layout, including headers and string padding.
// Charging here keeps worker allocation independent of copying policy.
func (c *asyncCopier) charge(v Value) {
	if c.budget != nil {
		c.budget.chargeCopy(int64(headerSize(heapHeader(v))) * 8)
	}
}

func (c *asyncCopier) boxData(data any) any {
	switch d := data.(type) {
	case boxFunction:
		return boxFunction{fn: c.function(d.fn)}
	case boxCell:
		return boxCell{c: c.cell(d.c)}
	case boxTable:
		return boxTable{t: c.table(d.t)}
	case boxAsyncTask:
		return boxAsyncTask{t: c.task(d.t)}
	case boxBuiltin, boxGensym, boxGoFunc, boxGoValue, boxStream, boxListener:
		// The destination still gets its own Boxed wrapper and mark bit.
		return data
	default:
		raise(symTypeError, "par/wait: cannot copy boxed value of type %T", data)
		return nil
	}
}

func (c *asyncCopier) function(src *Function) *Function {
	if src == nil {
		return nil
	}
	if dst, ok := c.functions[src]; ok {
		return dst
	}
	dst := new(Function)
	*dst = *src // code and compiler capture metadata are immutable.
	c.functions[src] = dst
	dst.consts = c.valuesSlice(src.consts)
	dst.upvals = make([]*FrameCell, len(src.upvals))
	for i, up := range src.upvals {
		dst.upvals[i] = c.cell(up)
	}
	dst.env = c.module(src.env)
	if src.env == nil && c.source != nil {
		dst.env = c.ambientModule()
	}
	return dst
}

func (c *asyncCopier) cell(src *FrameCell) *FrameCell {
	if src == nil {
		return nil
	}
	if dst, ok := c.cells[src]; ok {
		return dst
	}
	dst := &FrameCell{}
	c.cells[src] = dst
	dst.v = c.value(src.v)
	return dst
}

func (c *asyncCopier) table(src *Table) *Table {
	if src == nil {
		return nil
	}
	if dst, ok := c.tables[src]; ok {
		return dst
	}
	dst := NewTable()
	c.tables[src] = dst
	dst.count = src.count
	// Copy the storage directly: Put would compare keys and can recurse
	// through cyclic cons/vector keys. Rebuild even the raw-key map so no
	// source heap address can survive if its key representation expands.
	for k, v := range src.m {
		dst.m[uint64(c.value(Value(k)))] = c.value(v)
	}
	for k, e := range src.hm {
		dst.hm[k] = tableEntry{key: c.value(e.key), val: c.value(e.val)}
	}
	dst.other = make([]tableEntry, len(src.other))
	for i, e := range src.other {
		dst.other[i] = tableEntry{key: c.value(e.key), val: c.value(e.val)}
	}
	return dst
}

func (c *asyncCopier) module(src *Module) *Module {
	c.checkContext()
	if src == nil {
		return nil
	}
	if dst, ok := c.modules[src]; ok {
		return dst
	}
	dst := &Module{Path: src.Path, Dir: src.Dir, loading: src.loading, sealed: src.sealed}
	c.modules[src] = dst
	dst.fallback = c.module(src.fallback)
	if src.fallback == nil && !src.sealed && c.source != nil {
		dst.fallback = c.ambientModule()
	}
	dst.globals = c.valuesSlice(src.globals)
	dst.macros = c.valuesSlice(src.macros)
	dst.Exports = c.table(src.Exports)
	dst.exportsV = c.value(src.exportsV)
	return dst
}

func (c *asyncCopier) task(src *asyncTask) *asyncTask {
	if src == nil {
		return nil
	}
	if dst, ok := c.tasks[src]; ok {
		return dst
	}
	dst := &asyncTask{owner: c.dst, state: src.state}
	c.tasks[src] = dst
	// Never inspect the shared worker result here: the worker may still be
	// running. Only an already imported, source-local cache is safe to copy.
	if src.imported {
		dst.result = c.value(src.result)
		dst.imported = true
	}
	return dst
}
