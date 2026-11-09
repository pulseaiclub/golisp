package golisp

import (
	"context"
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

	globals   []Value    // global bindings by symbol id; zero means unbound
	macros    []Value    // macro functions by symbol id
	builtins  []*Builtin // builtin metadata by symbol id
	constants []bool     // immutable core binding flags by symbol id

	stdout io.Writer
	reader *strings.Reader // for repl

	macroExpandEnabled bool

	// ctx is the context of the active host invocation.
	ctx context.Context
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

func (g *Interp) ensureSymbolState(id uint32) {
	n := int(id) + 1
	if len(g.globals) >= n {
		return
	}
	g.globals = append(g.globals, make([]Value, n-len(g.globals))...)
	g.macros = append(g.macros, make([]Value, n-len(g.macros))...)
	g.builtins = append(g.builtins, make([]*Builtin, n-len(g.builtins))...)
	g.constants = append(g.constants, make([]bool, n-len(g.constants))...)
}

func (g *Interp) getGlobal(id uint32) Value {
	g.ensureSymbolState(id)
	v := g.globals[id]
	if v == 0 {
		raise(symUnboundError, "variable %s has no value", SymName(SymbolValue(id)))
	}
	return v
}

func (g *Interp) setGlobal(id uint32, v Value) {
	g.ensureSymbolState(id)
	g.globals[id] = v
}

func (g *Interp) symIsMacro(id uint32) bool {
	g.ensureSymbolState(id)
	return g.macros[id] != 0
}

func (g *Interp) registerMacro(id uint32, fn Value) {
	g.ensureSymbolState(id)
	g.macros[id] = fn
	g.globals[id] = fn
}

func (g *Interp) macroExpand(x Value) Value {
	id := SymID(carFast(x))
	g.ensureSymbolState(id)
	mfn := g.macros[id]
	args := listToSlice(cdrFast(x))
	return g.applyValue(mfn, args)
}

func (g *Interp) builtinOf(id uint32) *Builtin {
	g.ensureSymbolState(id)
	return g.builtins[id]
}

// --- evaluation entry points ---

// Eval compiles expr and runs it.
func (g *Interp) Eval(x Value) (Value, error) {
	return g.EvalTop(x)
}

func (g *Interp) EvalString(src string) (Value, error) {
	return g.EvalStringContext(g.Context(), src)
}

// EvalTop evaluates a top-level form, handling define-macro etc.
func (g *Interp) EvalTop(x Value) (result Value, err error) {
	sp0 := g.sp
	tryTop := len(g.tryStack)
	cellTop := len(g.cellPool)
	g.push(x)
	defer func() {
		var recovered any
		if r := recover(); r != nil {
			recovered = r
		}
		g.sp = sp0
		if len(g.tryStack) > tryTop {
			g.tryStack = g.tryStack[:tryTop]
		}
		g.popCells(cellTop)
		if recovered != nil {
			if le, ok := recovered.(*LispError); ok {
				err = le
				result = Nil
			} else {
				panic(recovered)
			}
		}
	}()
	c := &compiler{g: g}
	fn := c.CompileTop(x)

	fnValue := g.box(boxFunction{fn})
	g.push(fnValue)
	if Debug {
		fmt.Fprint(g.stdout, g.Dump(fn))
	}
	result = g.apply(fn, nil)
	return result, nil
}

// EvalContext evaluates one top-level form after checking the caller's context.
// An interpreter remains single-owner during an evaluation call.
func (g *Interp) EvalContext(ctx context.Context, x Value) (Value, error) {
	return g.withInvocation(ctx, func() (Value, error) {
		g.checkContext()
		return g.EvalTop(x)
	})
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
