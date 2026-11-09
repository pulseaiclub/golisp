package golisp

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Interp holds all interpreter state.
type Interp struct {
	heap  *heap
	stack []Value // value stack
	sp    int

	// frames for the VM (parallel arrays to avoid allocation)
	tryStack []tryFrame
	cellPool []*FrameCell // GC roots for all frame cells

	globals []Value // cache of global bindings by symbol id (lazily filled)

	stdout io.Writer
	reader *strings.Reader // for repl

	macroExpandEnabled bool
}

type tryFrame struct {
	sp      int
	cellTop int
	handler Value
}

const initialStackSize = 4096

func NewInterp() *Interp {
	internWellKnown()
	g := &Interp{
		heap:   newHeap(),
		stack:  make([]Value, initialStackSize),
		stdout: os.Stdout,
	}
	g.installCore()
	g.installStdStreams()
	if err := g.loadStandardLibrary(); err != nil {
		panic(err)
	}
	if err := g.loadMacroLibrary(); err != nil {
		panic(err)
	}
	return g
}

// --- stack helpers ---

func (g *Interp) push(v Value) {
	if g.sp >= len(g.stack) {
		ns := make([]Value, len(g.stack)*2)
		copy(ns, g.stack)
		g.stack = ns
	}
	g.stack[g.sp] = v
	g.sp++
}

func (g *Interp) pop() Value {
	g.sp--
	return g.stack[g.sp]
}

// --- globals ---

func (g *Interp) getGlobal(id uint32) Value {
	symMu.RLock()
	defer symMu.RUnlock()
	v := symNames[id].binding
	if v == 0 {
		raise(symUnboundError, "variable %s has no value", symNames[id].name)
	}
	return v
}

func (g *Interp) setGlobal(id uint32, v Value) {
	symMu.Lock()
	symNames[id].binding = v
	symMu.Unlock()
}

func (g *Interp) symIsMacro(id uint32) bool {
	symMu.RLock()
	defer symMu.RUnlock()
	return symNames[id].macro
}

func (g *Interp) registerMacro(id uint32, fn Value) {
	symMu.Lock()
	symNames[id].macro = true
	symNames[id].function = fn
	symNames[id].binding = fn
	symMu.Unlock()
}

func (g *Interp) macroExpand(x Value) Value {
	id := SymID(carFast(x))
	symMu.RLock()
	mfn := symNames[id].function
	symMu.RUnlock()
	args := listToSlice(cdrFast(x))
	return g.applyValue(mfn, args)
}

func (g *Interp) builtinOf(id uint32) *Builtin {
	symMu.RLock()
	defer symMu.RUnlock()
	return symNames[id].builtin
}

// --- evaluation entry points ---

// Eval compiles expr and runs it.
func (g *Interp) Eval(x Value) (Value, error) {
	return g.EvalTop(x)
}

func (g *Interp) EvalString(src string) (Value, error) {
	forms, err := g.ReadAll(src)
	if err != nil {
		return Nil, err
	}
	var result Value = Nil
	for _, f := range forms {
		result, err = g.EvalTop(f)
		if err != nil {
			return Nil, err
		}
	}
	return result, nil
}

// EvalTop evaluates a top-level form, handling define-macro etc.
func (g *Interp) EvalTop(x Value) (result Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			if le, ok := r.(*LispError); ok {
				err = le
				result = Nil
				return
			}
			panic(r)
		}
	}()
	c := &compiler{g: g}
	fn := c.CompileTop(x)
	if Debug {
		fmt.Fprint(g.stdout, g.Dump(fn))
	}
	result = g.apply(fn, nil)
	return result, nil
}

// LoadFile reads and evaluates a lisp file.
func (g *Interp) LoadFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return &LispError{kind: symIOError, msg: fmt.Sprintf("could not open %q", path)}
	}
	forms, err := g.ReadAll(string(data))
	if err != nil {
		return err
	}
	for _, f := range forms {
		if _, err := g.EvalTop(f); err != nil {
			return fmt.Errorf("in file %s: %w", path, err)
		}
	}
	return nil
}

// --- function application ---

func listToSlice(lst Value) []Value {
	var out []Value
	for IsCons(lst) {
		out = append(out, carFast(lst))
		lst = cdrFast(lst)
	}
	return out
}
