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
}

func newAsyncCopier(dst *Interp, ctx context.Context) *asyncCopier {
	return &asyncCopier{
		dst: dst, ctx: ctx,
		values:    make(map[Value]Value),
		functions: make(map[*Function]*Function),
		cells:     make(map[*FrameCell]*FrameCell),
		tables:    make(map[*Table]*Table),
		modules:   make(map[*Module]*Module),
		tasks:     make(map[*asyncTask]*asyncTask),
	}
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
	c := newAsyncCopier(child, ctx)
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
	switch heapType(v) {
	case tCons:
		dst := g.Cons(Nil, Nil)
		c.values[v] = dst
		SetCar(dst, c.value(carFast(v)))
		SetCdr(dst, c.value(cdrFast(v)))
		return dst
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
	// A nil environment keeps the language's ambient-global semantics: when
	// a returned closure is imported by wait, its free global names resolve
	// in the receiving interpreter. Captured cells and explicit module
	// namespaces are copied, not rebound. Module fallback globals likewise
	// belong to the receiver. No task globals are merged into the receiver.
	dst.env = c.module(src.env)
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
	dst := &Module{Path: src.Path, Dir: src.Dir, loading: src.loading}
	c.modules[src] = dst
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
