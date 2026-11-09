package interp

import (
	"fmt"
	"strings"
)

// The compiler: s-expressions -> bytecode. Modeled on femtolisp's
// compiler.lsp.

// --- bytecode emitter ---

type emitter struct {
	code    []uint8
	consts  []Value
	labels  map[int]int
	fixups  []fixup
	nextLbl int
}

type fixup struct {
	pos   int
	label int
}

func newEmitter() *emitter {
	return &emitter{labels: make(map[int]int)}
}

func (e *emitter) emit(op uint8) { e.code = append(e.code, op) }

func (e *emitter) emitU8(op uint8, a uint8) {
	e.code = append(e.code, op, a)
}

func (e *emitter) emitU16(op uint8, a uint16) {
	e.code = append(e.code, op, uint8(a), uint8(a>>8))
}

func (e *emitter) emitU32(op uint8, a uint32) {
	e.code = append(e.code, op, uint8(a), uint8(a>>8), uint8(a>>16), uint8(a>>24))
}

func (e *emitter) emitRawU32(a uint32) {
	e.code = append(e.code, uint8(a), uint8(a>>8), uint8(a>>16), uint8(a>>24))
}

func (e *emitter) emit2U8(op uint8, a, b uint8) {
	e.code = append(e.code, op, a, b)
}

func (e *emitter) emitJump(op uint8, l int) {
	e.code = append(e.code, op, 0, 0, 0, 0)
	e.fixups = append(e.fixups, fixup{pos: len(e.code) - 4, label: l})
}

func (e *emitter) newLabel() int {
	e.nextLbl++
	return e.nextLbl
}

func (e *emitter) markLabel(l int) {
	e.labels[l] = len(e.code)
}

func (e *emitter) constIdx(v Value) int {
	for i, c := range e.consts {
		if Equal(c, v) && sameTag(c, v) {
			return i
		}
	}
	e.consts = append(e.consts, v)
	return len(e.consts) - 1
}

func sameTag(a, b Value) bool {
	if IsHeap(a) != IsHeap(b) {
		return false
	}
	if IsHeap(a) {
		return heapType(a) == heapType(b)
	}
	return uint64(a)&tagMask == uint64(b)&tagMask
}

func (e *emitter) fixup() {
	for _, f := range e.fixups {
		target, ok := e.labels[f.label]
		if !ok {
			panic("compiler: unresolved label")
		}
		rel := target - (f.pos + 4)
		e.code[f.pos] = uint8(rel)
		e.code[f.pos+1] = uint8(rel >> 8)
		e.code[f.pos+2] = uint8(rel >> 16)
		e.code[f.pos+3] = uint8(rel >> 24)
	}
	e.threadJumps()
}

// threadJumps implements the bytecode equivalent of main.lisp's
// optimize-jumps. A jump whose target is an unconditional jump is retargeted
// to the final destination. Labels are already resolved, so this is a cheap
// post-link pass and does not change stack behavior.
func (e *emitter) threadJumps() {
	for _, f := range e.fixups {
		if f.pos <= 0 || f.pos+4 > len(e.code) {
			continue
		}
		op := e.code[f.pos-1]
		if op != opJmp {
			continue
		}
		target := f.pos + 4 + int(int32(uint32(e.code[f.pos])|uint32(e.code[f.pos+1])<<8|uint32(e.code[f.pos+2])<<16|uint32(e.code[f.pos+3])<<24))
		for target >= 0 && target < len(e.code) && e.code[target] == opJmp && target+5 <= len(e.code) {
			rel := int(int32(uint32(e.code[target+1]) | uint32(e.code[target+2])<<8 | uint32(e.code[target+3])<<16 | uint32(e.code[target+4])<<24))
			target += 5 + rel
		}
		rel := target - (f.pos + 4)
		e.code[f.pos] = uint8(rel)
		e.code[f.pos+1] = uint8(rel >> 8)
		e.code[f.pos+2] = uint8(rel >> 16)
		e.code[f.pos+3] = uint8(rel >> 24)
	}
}

// --- compiler state ---

type compiler struct {
	g          *Interp
	curFn      *Function // function currently being compiled (for capture lookup)
	atTopLevel bool      // true when compiling the outermost thunk body
	env        *Module   // module namespace being compiled into (nil = ambient)
}

// The following helpers resolve names in the namespace of the code being
// compiled: the module namespace first, then the ambient globals.

func (c *compiler) symIsMacro(id uint32) bool {
	if c.env != nil && c.env.getMacro(id) != 0 {
		return true
	}
	return c.g.symIsMacro(id)
}

func (c *compiler) macroExpand(x Value) Value {
	if c.env != nil {
		if mfn := c.env.getMacro(SymID(carFast(x))); mfn != 0 {
			args := listToSlice(cdrFast(x))
			return c.g.applyValue(mfn, args)
		}
	}
	return c.g.macroExpand(x)
}

func (c *compiler) registerMacro(id uint32, fn Value) {
	if c.env != nil {
		c.env.setMacro(id, fn)
		return
	}
	c.g.registerMacro(id, fn)
}

func (c *compiler) symbolValue(id uint32) (Value, bool) {
	if c.env != nil {
		if v := c.env.get(id); v != 0 {
			return v, true
		}
	}
	return c.g.symbolValue(id)
}

// env: stack of frames; each frame is list of symbol ids.
// env[0] = innermost (current function's args + internal defines).

// captureIdx reports the capture index of a slot in the function currently
// being compiled, if that slot has a cell.
func (c *compiler) captureIdx(slot int) (int, bool) {
	if c.curFn == nil || c.curFn.capBySlot == nil {
		return 0, false
	}
	ci, ok := c.curFn.capBySlot[slot]
	return ci, ok
}

func (c *compiler) lookupSym(id uint32, env [][]uint32) (level, slot int, found bool) {
	for lev := 0; lev < len(env); lev++ {
		for i, s := range env[lev] {
			if s == id {
				return lev, i, true
			}
		}
	}
	return 0, 0, false
}

func (c *compiler) Compile(lambda Value) *Function {
	return c.compileF(nil, c.preOptimize(c.expandLambdaForCompile(lambda)))
}

func (c *compiler) CompileExpr(expr Value) *Function {
	g := c.g
	lambda := g.list3(SymbolValue(symLambda), Nil, c.preOptimize(c.expand(expr)))
	return c.compileF(nil, lambda)
}

// CompileTop compiles a top-level form (no enclosing env).
func (c *compiler) CompileTop(expr Value) *Function {
	g := c.g
	c.atTopLevel = true
	expanded := c.preOptimize(c.expand(expr))
	lambda := g.list3(SymbolValue(symLambda), Nil, expanded)
	fn := c.compileF(nil, lambda)
	c.atTopLevel = false
	return fn
}

// freeVarInfo: which of lambda's free vars come from env, and how.
type fvKind int

const (
	fvNone fvVar = iota
)

type fvVar int

// compileF compiles (lambda args body... . name) in env.
// Returns the prototype and the list of (level,slot) captures it needs.
func (c *compiler) compileF(env [][]uint32, lambda Value) *Function {
	args := cadr(lambda)
	body := cddr(lambda)
	// A function name is encoded as the improper tail of the lambda body.
	// The dotted tail of the formal list is handled separately below as the
	// variadic rest argument.
	selfName := uint32(0)
	hasSelf := false
	if IsCons(body) {
		tail := body
		for IsCons(cdrFast(tail)) {
			tail = cdrFast(tail)
		}
		if IsSymbol(cdrFast(tail)) && cdrFast(tail) != Nil {
			selfName = SymID(cdrFast(tail))
			hasSelf = true
			SetCdr(tail, Nil)
		}
	}

	// parse args; improper list tail (after .) is the self-name marker.
	var argSyms []uint32
	variadic := false
	nreq := 0
	type optArg struct {
		slot int
		def  Value
	}
	var optArgs []optArg

	p := args
	if IsSymbol(p) && p != Nil {
		// A lambda with a symbol as its formal list is fully variadic.
		argSyms = append(argSyms, SymID(p))
		variadic = true
		p = Nil
	}
	for IsCons(p) {
		a := carFast(p)
		if IsSymbol(a) && a == Symbol(".") {
			rest := cadr(p)
			argSyms = append(argSyms, SymID(rest))
			variadic = true
			p = Nil
			break
		}
		if IsCons(a) {
			optArgs = append(optArgs, optArg{slot: len(argSyms), def: cadr(a)})
			argSyms = append(argSyms, SymID(carFast(a)))
		} else if IsSymbol(a) {
			argSyms = append(argSyms, SymID(a))
			if len(optArgs) == 0 {
				nreq++
			}
		}
		p = cdrFast(p)
	}
	if IsSymbol(p) && p != Nil {
		// A dotted tail in a formal list is the rest argument. The
		// compiler's parser represents the dotted reader syntax as an
		// improper list whose tail is this symbol.
		argSyms = append(argSyms, SymID(p))
		variadic = true
	}

	// internal defines extend the frame
	frame := append([]uint32(nil), argSyms...)
	bodyForms := body
	for q := bodyForms; IsCons(q); q = cdrFast(q) {
		f := carFast(q)
		if IsCons(f) && carFast(f) == SymbolValue(symDefine) {
			nm := cadr(f)
			if IsCons(nm) {
				nm = carFast(nm)
			}
			if IsSymbol(nm) {
				frame = append(frame, SymID(nm))
			}
		} else {
			break
		}
	}
	// self-name is visible in the frame too
	if hasSelf {
		frame = append(frame, selfName)
	}

	newEnv := append([][]uint32{frame}, env...)

	// --- free-variable analysis ---
	// frameUse[symID] = true if the body (outside inner lambdas) references
	// symID and symID is in frame.
	// innerFree[symID] = symID referenced free inside some inner lambda.
	// outerUsed[symID] = symID referenced anywhere but not bound in frame.
	frameSet := make(map[uint32]bool, len(frame))
	for _, id := range frame {
		frameSet[id] = true
	}
	frameUse := map[uint32]bool{}
	innerFree := map[uint32]bool{}
	outerUsed := map[uint32]bool{}

	// collect free vars of an inner lambda body, marking frame vars in
	// innerFree and non-frame, non-lambda-bound vars in outerUsed.
	var collectFree func(x Value, bound map[uint32]bool)
	collectFree = func(x Value, bound map[uint32]bool) {
		if IsSymbol(x) {
			id := SymID(x)
			if bound[id] {
				return
			}
			if frameSet[id] {
				// A variable of this frame that an inner lambda uses free
				// needs a cell here. It is not an outer variable for this
				// function, so it does not appear in outerUsed.
				innerFree[id] = true
			} else if _, _, ok := c.lookupSym(id, env); ok {
				outerUsed[id] = true
			}
			return
		}
		if !IsCons(x) {
			return
		}
		h := carFast(x)
		if IsSymbol(h) {
			hid := SymID(h)
			if hid == symQuote {
				return
			}
			if hid == symLambda {
				// A lambda boundary resets the bound set: variables of the
				// enclosing frame are exactly the free variables we want to
				// record, so they must not be considered bound here.
				nb := make(map[uint32]bool, 8)
				a := cadr(x)
				for q := a; IsCons(q); q = cdrFast(q) {
					s := carFast(q)
					if IsCons(s) {
						s = carFast(s)
					}
					if IsSymbol(s) {
						nb[SymID(s)] = true
					}
				}
				if IsSymbol(a) && a != Nil {
					nb[SymID(a)] = true
				}
				// internal defines of the inner lambda
				for q := cddr(x); IsCons(q); q = cdrFast(q) {
					f := carFast(q)
					if IsCons(f) && carFast(f) == SymbolValue(symDefine) {
						nm := cadr(f)
						if IsCons(nm) {
							nm = carFast(nm)
						}
						if IsSymbol(nm) {
							nb[SymID(nm)] = true
						}
					} else {
						break
					}
				}
				for q := cddr(x); IsCons(q); q = cdrFast(q) {
					collectFree(carFast(q), nb)
				}
				return
			}
		}
		for q := x; IsCons(q); q = cdrFast(q) {
			collectFree(carFast(q), bound)
		}
	}

	// walk body; bound starts as frame set
	walkBound := make(map[uint32]bool, len(frame))
	for _, id := range frame {
		walkBound[id] = true
	}
	var walkBody func(x Value)
	walkBody = func(x Value) {
		if IsSymbol(x) {
			id := SymID(x)
			if frameSet[id] {
				frameUse[id] = true
			} else if _, _, ok := c.lookupSym(id, env); ok {
				outerUsed[id] = true
			}
			return
		}
		if !IsCons(x) {
			return
		}
		h := carFast(x)
		if IsSymbol(h) {
			hid := SymID(h)
			if hid == symQuote {
				return
			}
			if hid == symLambda {
				// The nested lambda's own formals are bound inside it, but
				// nothing from this frame is; start from an empty set.
				collectFree(x, map[uint32]bool{})
				return
			}
		}
		for q := x; IsCons(q); q = cdrFast(q) {
			walkBody(carFast(q))
		}
	}
	for q := bodyForms; IsCons(q); q = cdrFast(q) {
		walkBody(carFast(q))
	}
	_ = frameUse

	// A frame var is captured if some inner lambda references it.
	ownCaptured := map[int]bool{}
	for i, id := range frame {
		if innerFree[id] {
			ownCaptured[i] = true
		}
	}

	// captures from outer env: outerUsed syms that resolve lexically.
	type capture struct {
		symID uint32
		lev   int
		slot  int
	}
	var captures []capture
	for id := range outerUsed {
		if lev, slot, ok := c.lookupSym(id, env); ok {
			captures = append(captures, capture{id, lev, slot})
		}
	}
	// deterministic order
	for i := 0; i < len(captures); i++ {
		for j := i + 1; j < len(captures); j++ {
			if captures[j].symID < captures[i].symID {
				captures[i], captures[j] = captures[j], captures[i]
			}
		}
	}
	capIdxBySym := make(map[uint32]int, len(captures))
	for i, cap := range captures {
		capIdxBySym[cap.symID] = i
	}

	e := newEmitter()
	fn := &Function{
		name:      "lambda",
		nreq:      nreq,
		nargs:     len(argSyms),
		variadic:  variadic,
		hasOpt:    len(optArgs) > 0,
		hasSelf:   hasSelf,
		selfRef:   hasSelf,
		selfID:    selfName,
		frameSize: len(frame),
		env:       c.env,
	}

	// fill capture metadata
	for i := range frame {
		if ownCaptured[i] {
			if fn.capBySlot == nil {
				fn.capBySlot = map[int]int{}
			}
			fn.capBySlot[i] = len(fn.capSlots)
			fn.capSlots = append(fn.capSlots, i)
		}
	}
	// capSrc: for each capture (upval index i), how to get the cell from
	// the *parent* frame. We need to know how the parent accesses the var:
	//   lev==1: it's the parent's own frame slot.
	//   lev>1:  the parent itself has it as an upval; we need its index in
	//           the parent's upvals, which equals the parent's capture index
	//           for that symbol. We resolve this by looking at the parent's
	//           capBySym — but compileF doesn't know the parent Function.
	// To keep it simple we record the symID for each capture; the closure
	// opcode stores (lev,slot,symID) triples and the VM resolves the cell
	// by walking: if the var is in parent's frame -> parent capBySlot cell;
	// else find parent's upval by symID via parent fn.capSymIDs.
	fn.capSymIDs = make([]uint32, len(captures))
	fn.capSrc = make([]uint32, len(captures))
	for i, cap := range captures {
		fn.capSymIDs[i] = cap.symID
		fn.capSrc[i] = uint32(cap.lev)<<16 | uint32(cap.slot)
	}
	fn.capBySym = capIdxBySym
	outerFn := c.curFn
	c.curFn = fn

	// prologue
	if variadic {
		e.emitU8(opVArgc, uint8(len(argSyms)))
	} else if len(optArgs) > 0 {
		e.emitU8(opOptArgs, uint8(nreq))
		e.emit(uint8(len(argSyms)))
	} else {
		e.emitU8(opArgc, uint8(len(argSyms)))
	}

	// optional arg defaults
	if len(optArgs) > 0 {
		for _, oa := range optArgs {
			next := e.newLabel()
			// brbound slot -> next (skip default)
			e.code = append(e.code, opBrBound, uint8(oa.slot), uint8(oa.slot>>8), 0, 0, 0, 0)
			e.fixups = append(e.fixups, fixup{pos: len(e.code) - 4, label: next})
			defEnv := append([][]uint32{frame[:oa.slot]}, env...)
			c.compileIn(e, defEnv, false, oa.def)
			e.emitU16(opSetA, uint16(oa.slot))
			e.emit(opPop)
			e.markLabel(next)
		}
	}

	// body: rewrite defines to set!
	c.compileBody(e, newEnv, bodyForms, fn)
	e.emit(opRet)
	e.fixup()

	fn.code = e.code
	fn.consts = e.consts
	c.curFn = outerFn
	return fn
}

// compileBegin compiles a list of forms; the last is in tail position if
// tail is true.
func (c *compiler) compileBegin(e *emitter, env [][]uint32, tail bool, forms Value) {
	if !IsCons(forms) {
		c.compileIn(e, env, tail, Nil)
		return
	}
	for {
		f := carFast(forms)
		rest := cdrFast(forms)
		if !IsCons(rest) {
			c.compileIn(e, env, tail, f)
			return
		}
		c.compileIn(e, env, false, f)
		e.emit(opPop)
		forms = rest
	}
}

// compileBody compiles body forms, treating internal defines as set!.
func (c *compiler) compileBody(e *emitter, env [][]uint32, body Value, fn *Function) {
	if !IsCons(body) {
		e.emit(opLoadNil)
		return
	}
	for {
		f := carFast(body)
		rest := cdrFast(body)
		last := !IsCons(rest)
		if IsCons(f) && carFast(f) == SymbolValue(symDefine) {
			if c.atTopLevel {
				c.compileDefine(e, nil, false, f)
			} else {
				c.compileDefineInternal(e, env, f)
			}
			if !last {
				e.emit(opPop)
			}
		} else {
			c.compileIn(e, env, last, f)
			if !last {
				e.emit(opPop)
			}
		}
		if last {
			return
		}
		body = rest
	}
}

func (c *compiler) compileDefineInternal(e *emitter, env [][]uint32, x Value) {
	g := c.g
	form := cadr(x)
	body := cddr(x)
	if IsSymbol(form) {
		var val Value = Nil
		if IsCons(body) {
			val = carFast(body)
		}
		c.compileIn(e, env, false, val)
		c.compileSym(e, env, form, true)
		return
	}
	name := carFast(form)
	args := cdrFast(form)
	lambda := g.Cons(SymbolValue(symLambda), g.Cons(args, appendImproper(g, body, name)))
	c.compileIn(e, env, false, lambda)
	c.compileSym(e, env, name, true)
}

func appendImproper(g *Interp, lst, tail Value) Value {
	if !IsCons(lst) {
		return tail
	}
	var out Value = Nil
	var tl Value
	for IsCons(lst) {
		c := g.Cons(carFast(lst), Nil)
		if out == Nil {
			out = c
		} else {
			SetCdr(tl, c)
		}
		tl = c
		lst = cdrFast(lst)
	}
	if out == Nil {
		return tail
	}
	SetCdr(tl, tail)
	return out
}

func (c *compiler) compileIn(e *emitter, env [][]uint32, tail bool, x Value) {
	g := c.g
	switch {
	case IsSymbol(x):
		// Keywords such as :write evaluate to themselves.
		if isKeyword(x) {
			c.compileConst(e, x)
			return
		}
		c.compileSym(e, env, x, false)
	case !IsCons(x):
		c.compileConst(e, x)
	default:
		head := carFast(x)
		if IsSymbol(head) {
			id := SymID(head)
			switch id {
			case symQuote:
				c.compileConst(e, cadr(x))
				return
			case symIf:
				c.compileIf(e, env, tail, x)
				return
			case symBegin:
				c.compileBegin(e, env, tail, cdrFast(x))
				return
			case symSet:
				c.compileIn(e, env, false, caddr(x))
				c.compileSym(e, env, cadr(x), true)
				return
			case symLambda:
				fn := c.compileF(env, x)
				// A lambda with no captures is immutable and can reuse its
				// prototype directly. Capturing lambdas still need a fresh
				// closure object with the current upvalue cells.
				proto := g.box(boxFunction{fn})
				e.emitU16(opLoadV, uint16(e.constIdx(proto)))
				if len(fn.capSrc) > 0 {
					e.emitU8(opClosure, uint8(len(fn.capSrc)))
				}
				return
			case symAnd:
				c.compileShortCircuit(e, env, tail, cdrFast(x), True, opBrf)
				return
			case symOr:
				c.compileShortCircuit(e, env, tail, cdrFast(x), False, opBrt)
				return
			case symWhile:
				c.compileWhile(e, env, x)
				return
			case symCond:
				c.compileIn(e, env, tail, c.condToIf(cdrFast(x)))
				return
			case symLet:
				c.compileLet(e, env, tail, x)
				return
			case symDoTimes:
				c.compileDotimes(e, env, tail, x)
				return
			case symProg1:
				c.compileIn(e, env, false, cadr(x))
				rest := cddr(x)
				if IsCons(rest) {
					c.compileBegin(e, env, false, rest)
					e.emit(opPop)
				}
				return
			case symQuasiQuote:
				c.compileIn(e, env, tail, c.expandQQ(cadr(x)))
				return
			case symTryCatch:
				c.compileIn(e, env, false, cadr(x))
				c.compileIn(e, env, false, caddr(x))
				e.emit(opTryCatch)
				return
			case symLabel:
				c.compileLabel(e, env, tail, x)
				return
			case symDefine:
				c.compileDefine(e, env, tail, x)
				return
			case symDefineMac:
				c.compileDefineMacro(e, env, x)
				return
			}
			if c.symIsMacro(id) {
				exp := c.macroExpand(x)
				c.compileIn(e, env, tail, exp)
				return
			}
		}
		c.compileApp(e, env, tail, x)
	}
}

func (c *compiler) compileConst(e *emitter, x Value) {
	switch {
	case x == Nil:
		e.emit(opLoadNil)
	case x == True:
		e.emit(opLoadT)
	case x == False:
		e.emit(opLoadF)
	case IsFixnum(x) && Fixnum(x) == 0:
		e.emit(opLoad0)
	case IsFixnum(x) && Fixnum(x) == 1:
		e.emit(opLoad1)
	case IsFixnum(x) && Fixnum(x) >= -128 && Fixnum(x) <= 127:
		e.emitU8(opLoadI8, uint8(int8(Fixnum(x))))
	default:
		e.emitU16(opLoadV, uint16(e.constIdx(x)))
	}
}

// isKeyword reports whether v is a :keyword symbol, which is self-evaluating.
func isKeyword(v Value) bool {
	return IsSymbol(v) && SymName(v)[0] == ':'
}

// splitDotted splits "a.b" into ("a", "b"). Only the first dot is significant
// so nested access such as obj.Field.Method stays expressible.
func splitDotted(name string) (prefix, member string, ok bool) {
	i := strings.IndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return "", "", false
	}
	return name[:i], name[i+1:], true
}

// dottedPrefix reports whether x is a dotted symbol whose prefix is not a
// lexical variable. Such a symbol is resolved at run time: it is a Go member
// access when the prefix holds a Go value, and a plain global otherwise.
func (c *compiler) dottedPrefix(x Value, env [][]uint32) (prefix, member string, ok bool) {
	prefix, member, ok = splitDotted(SymName(x))
	if !ok {
		return "", "", false
	}
	if _, _, bound := c.lookupSym(SymID(Symbol(prefix)), env); bound {
		return "", "", false
	}
	return prefix, member, true
}

func (c *compiler) emitGoMember(e *emitter, x Value, callable bool) {
	prefix, member, _ := splitDotted(SymName(x))
	e.code = append(e.code, opGoMember)
	id := SymID(Symbol(prefix))
	e.code = append(e.code,
		uint8(id), uint8(id>>8), uint8(id>>16), uint8(id>>24))
	idx := e.constIdx(c.g.NewString(member))
	e.code = append(e.code, uint8(idx), uint8(idx>>8))
	full := SymID(x)
	e.code = append(e.code,
		uint8(full), uint8(full>>8), uint8(full>>16), uint8(full>>24))
	if callable {
		e.code = append(e.code, 1)
	} else {
		e.code = append(e.code, 0)
	}
}

func (c *compiler) emitGoSet(e *emitter, x Value) {
	prefix, member, _ := splitDotted(SymName(x))
	e.code = append(e.code, opGoSet)
	id := SymID(Symbol(prefix))
	e.code = append(e.code,
		uint8(id), uint8(id>>8), uint8(id>>16), uint8(id>>24))
	idx := e.constIdx(c.g.NewString(member))
	e.code = append(e.code, uint8(idx), uint8(idx>>8))
}

// compileSym emits a load (set=false) or store (set=true) for symbol x.
func (c *compiler) compileSym(e *emitter, env [][]uint32, x Value, set bool) {
	if _, _, ok := c.dottedPrefix(x, env); ok {
		if set {
			c.emitGoSet(e, x)
		} else {
			c.emitGoMember(e, x, false)
		}
		return
	}
	id := SymID(x)
	lev, slot, ok := c.lookupSym(id, env)
	if ok {
		if lev == 0 {
			// A frame slot that is captured by an inner closure must be
			// read from its cell; the raw stack slot may be stale.
			if ci, captured := c.captureIdx(slot); captured {
				// lev 1 selects this frame's own cell, not an upval.
				if set {
					e.emit2U8(opSetC, 1, uint8(ci))
				} else {
					e.emit2U8(opLoadC, 1, uint8(ci))
				}
				return
			}
			if set {
				e.emitU16(opSetA, uint16(slot))
			} else {
				switch slot {
				case 0:
					e.emit(opLoadA0)
				case 1:
					e.emit(opLoadA1)
				default:
					e.emitU16(opLoadA, uint16(slot))
				}
			}
		} else {
			// captured variable: resolve to this function's upval index
			ci := -1
			if c.curFn != nil && c.curFn.capBySym != nil {
				if ix, ok := c.curFn.capBySym[id]; ok {
					ci = ix
				}
			}
			if ci < 0 {
				// not captured (analysis said unused) — shouldn't happen
				raise(symError, "compiler: %s not captured", SymName(x))
			}
			// lev 0 selects an upval captured from an enclosing frame.
			if set {
				e.emit2U8(opSetC, 0, uint8(ci))
			} else {
				e.emit2U8(opLoadC, 0, uint8(ci))
			}
		}
		return
	}
	if set {
		e.emitU32(opSetG, id)
	} else {
		e.emitU32(opLoadG, id)
	}
}

func (c *compiler) compileIf(e *emitter, env [][]uint32, tail bool, x Value) {
	test := cadr(x)
	then := caddr(x)
	els := Nil
	if IsCons(cdddr(x)) {
		els = cadddr(x)
	}
	if test == True {
		c.compileIn(e, env, tail, then)
		return
	}
	if test == False {
		c.compileIn(e, env, tail, els)
		return
	}
	elsel := e.newLabel()
	endl := e.newLabel()
	if rel, ok := c.numericRelation(test); ok {
		c.compileIn(e, env, false, rel.left)
		c.compileIn(e, env, false, rel.right)
		if rel.equal {
			e.emitJump(opBrNumEq, elsel)
		} else {
			e.emitJump(opBrNumLt, elsel)
		}
	} else {
		c.compileIn(e, env, false, test)
		e.emitJump(opBrf, elsel)
	}
	c.compileIn(e, env, tail, then)
	if tail {
		e.emit(opRet)
	} else {
		e.emitJump(opJmp, endl)
	}
	e.markLabel(elsel)
	c.compileIn(e, env, tail, els)
	e.markLabel(endl)
}

type numericRelation struct {
	left, right Value
	equal       bool
}

func (c *compiler) numericRelation(x Value) (numericRelation, bool) {
	if !IsCons(x) || !IsSymbol(carFast(x)) {
		return numericRelation{}, false
	}
	name := SymName(carFast(x))
	if name != "<" && name != "=" {
		return numericRelation{}, false
	}
	args := cdrFast(x)
	if listLen(args) != 2 {
		return numericRelation{}, false
	}
	return numericRelation{left: carFast(args), right: cadr(args), equal: name == "="}, true
}

func (c *compiler) compileWhile(e *emitter, env [][]uint32, x Value) {
	top := e.newLabel()
	end := e.newLabel()
	falsePath := e.newLabel()
	e.emit(opLoadNil)
	e.markLabel(top)
	c.compileIn(e, env, false, cadr(x))
	e.emitJump(opBrf, falsePath)
	body := cddr(x)
	if IsCons(body) {
		c.compileBegin(e, env, false, body)
	} else {
		e.emit(opLoadNil)
	}
	e.emit(opPop)
	e.emitJump(opJmp, top)
	e.markLabel(falsePath)
	e.emitJump(opJmp, end)
	e.markLabel(end)
	e.emit(opLoadNil)
}

func (c *compiler) compileShortCircuit(e *emitter, env [][]uint32, tail bool, forms Value, dflt Value, branch uint8) {
	if !IsCons(forms) {
		c.compileConst(e, dflt)
		return
	}
	if !IsCons(cdrFast(forms)) {
		c.compileIn(e, env, tail, carFast(forms))
		return
	}
	end := e.newLabel()
	c.compileIn(e, env, false, carFast(forms))
	e.emit(opDup)
	e.emitJump(branch, end)
	e.emit(opPop)
	c.compileShortCircuit(e, env, tail, cdrFast(forms), dflt, branch)
	e.markLabel(end)
}

func (c *compiler) condToIf(clauses Value) Value {
	g := c.g
	if !IsCons(clauses) {
		return Nil
	}
	clause := carFast(clauses)
	test := carFast(clause)
	body := cdrFast(clause)
	rest := cdrFast(clauses)
	if test == SymbolValue(internString("else")) || test == True {
		if IsCons(body) {
			return g.cons2(SymbolValue(symBegin), body)
		}
		return test
	}
	if !IsCons(body) {
		tmp := Symbol("%cond")
		return g.list3(
			SymbolValue(symLet),
			g.list2(g.list2(tmp, test), Nil),
			g.list4(SymbolValue(symIf), tmp, tmp, c.condToIf(rest)),
		)
	}
	return g.list4(SymbolValue(symIf), test, g.cons2(SymbolValue(symBegin), body), c.condToIf(rest))
}

func (c *compiler) compileLet(e *emitter, env [][]uint32, tail bool, x Value) {
	g := c.g
	p := cdrFast(x)
	if IsSymbol(carFast(p)) {
		// named let
		name := carFast(p)
		bindings := cadr(p)
		body := cddr(p)
		var vars, inits Value = Nil, Nil
		for b := bindings; IsCons(b); b = cdrFast(b) {
			bind := carFast(b)
			vars = g.Cons(carFast(bind), vars)
			inits = g.Cons(cadr(bind), inits)
		}
		vars = g.reverseList(vars)
		inits = g.reverseList(inits)
		lambda := g.Cons(SymbolValue(symLambda), g.Cons(vars, appendImproper(g, body, name)))
		// (let ((name nil)) (set! name lambda) (name . inits))
		let := g.list3(SymbolValue(symLet),
			g.list2(g.list2(name, Nil), Nil),
			g.list3(SymbolValue(symSet), name, lambda))
		call := g.Cons(name, inits)
		let = g.list3(SymbolValue(symLet),
			g.list2(g.list2(name, Nil), Nil),
			g.list4(SymbolValue(symBegin),
				g.list3(SymbolValue(symSet), name, lambda),
				call, Nil))
		_ = let
		// simpler: (label name lambda) applied to inits
		label := g.list3(SymbolValue(symLabel), name, lambda)
		call = g.Cons(label, inits)
		c.compileIn(e, env, tail, call)
		return
	}
	bindings := carFast(p)
	body := cdrFast(p)
	var vars, inits []Value
	for b := bindings; IsCons(b); b = cdrFast(b) {
		bind := carFast(b)
		if IsSymbol(bind) {
			vars = append(vars, bind)
			inits = append(inits, Nil)
		} else {
			vars = append(vars, carFast(bind))
			inits = append(inits, cadr(bind))
		}
	}
	varsV := Nil
	for i := len(vars) - 1; i >= 0; i-- {
		varsV = g.Cons(vars[i], varsV)
	}
	lambda := g.Cons(SymbolValue(symLambda), g.Cons(varsV, body))
	call := g.Cons(lambda, g.reverseListFromSlice(inits))
	// The list is (lambda init1 init2 ...), so append inits after the
	// lambda head; reverseListFromSlice already preserves source order.
	c.compileIn(e, env, tail, call)
}

func (c *compiler) compileDotimes(e *emitter, env [][]uint32, tail bool, x Value) {
	g := c.g
	spec := cadr(x)
	var_ := carFast(spec)
	cnt := cadr(spec)
	body := cddr(x)

	nSym := Symbol("%dotimes-n")
	loopVar := g.list2(var_, FixnumValue(0))
	nVar := g.list2(nSym, cnt)
	bindings := g.list2(loopVar, nVar)
	incr := g.list3(SymbolValue(symSet), var_,
		g.list3(Symbol("+"), var_, FixnumValue(1)))
	test := g.list3(Symbol("<"), var_, nSym)
	whileBody := Nil
	for p := body; IsCons(p); p = cdrFast(p) {
		whileBody = g.Cons(carFast(p), whileBody)
	}
	whileBody = g.reverseList(whileBody)
	whileBody = appendImproper(g, whileBody, g.list2(incr, Nil))
	whileForm := g.cons3(SymbolValue(symWhile), test, whileBody)
	let := g.list3(SymbolValue(symLet), bindings, whileForm)
	c.compileIn(e, env, tail, let)
}

func (c *compiler) compileLabel(e *emitter, env [][]uint32, tail bool, x Value) {
	g := c.g
	name := cadr(x)
	fnForm := caddr(x)
	bindings := g.list2(g.list2(name, Nil), Nil)
	setForm := g.list3(SymbolValue(symSet), name, fnForm)
	let := g.list4(SymbolValue(symLet), bindings, setForm, name)
	c.compileIn(e, env, tail, let)
}

func (c *compiler) compileDefine(e *emitter, env [][]uint32, tail bool, x Value) {
	g := c.g
	form := cadr(x)
	body := cddr(x)
	if IsSymbol(form) {
		var val Value = Nil
		if IsCons(body) {
			val = carFast(body)
		}
		c.compileIn(e, env, false, val)
		// top-level define (env empty) emits setg; internal define seta
		if len(env) == 0 {
			e.emitU32(opSetG, SymID(form))
		} else {
			c.compileSym(e, env, form, true)
		}
		return
	}
	name := carFast(form)
	args := cdrFast(form)
	lambda := g.Cons(SymbolValue(symLambda), g.Cons(args, appendImproper(g, body, name)))
	c.compileIn(e, env, false, lambda)
	if len(env) == 0 {
		e.emitU32(opSetG, SymID(name))
	} else {
		c.compileSym(e, env, name, true)
	}
}

func (c *compiler) compileDefineMacro(e *emitter, env [][]uint32, x Value) {
	g := c.g
	form := cadr(x)
	body := cddr(x)
	var name Value
	var lambda Value
	if IsSymbol(form) {
		name = form
		lambda = g.cons3(SymbolValue(symLambda), Nil, body)
	} else {
		name = carFast(form)
		lambda = g.cons3(SymbolValue(symLambda), cdrFast(form), body)
	}
	fn := c.CompileExpr(lambda)
	v := g.apply(fn, nil)
	c.registerMacro(SymID(name), v)
	e.emit(opLoadNil)
}

func (c *compiler) compileApp(e *emitter, env [][]uint32, tail bool, x Value) {
	g := c.g
	head := carFast(x)
	args := cdrFast(x)
	nargs := listLen(args)

	// builtin inlining
	if IsSymbol(head) {
		id := SymID(head)
		// A module may shadow a builtin; the ambient inline table must not
		// win over the module's own binding.
		if b := g.builtinOf(id); b != nil && b.opcode != 0 && !c.inEnv(id, env) &&
			(c.env == nil || c.env.get(id) == 0) {
			if c.compileBuiltinApp(e, env, b, args, nargs) {
				return
			}
		}

		// A function's named self reference is the common recursive-call
		// case. Emit a direct self opcode and avoid loading the closure.
		if c.curFn != nil && c.curFn.selfRef && c.selfNameMatches(head) {
			for p := args; IsCons(p); p = cdrFast(p) {
				c.compileIn(e, env, false, carFast(p))
			}
			if tail {
				e.emitU8(opSelfTCall, uint8(nargs))
			} else {
				e.emitU8(opSelfCall, uint8(nargs))
			}
			return
		}

		// A global binding that is a plain Lisp function can be constant
		// folded into a direct known-call. Mutable or Go bindings keep the
		// normal dynamic path.
		if !c.inEnv(id, env) {
			if target, ok := c.symbolValue(id); ok {
				if fn := AsFunction(target); fn != nil && !fn.hasSelf {
					idx := e.constIdx(target)
					for p := args; IsCons(p); p = cdrFast(p) {
						c.compileIn(e, env, false, carFast(p))
					}
					op := opKnownCall
					if tail {
						op = opKnownTCall
					}
					e.code = append(e.code, op, uint8(idx), uint8(idx>>8), uint8(nargs))
					return
				}
			}
		}
	}
	// Dotted head: resolve as a Go member in callable mode, then call.
	if IsSymbol(head) {
		if _, _, ok := c.dottedPrefix(head, env); ok {
			c.emitGoMember(e, head, true)
			for p := args; IsCons(p); p = cdrFast(p) {
				c.compileIn(e, env, false, carFast(p))
			}
			if tail {
				e.emitU8(opTCall, uint8(nargs))
			} else {
				e.emitU8(opCall, uint8(nargs))
			}
			return
		}
	}

	// cadr fusion
	if IsSymbol(head) && SymID(head) == internString("cadr") && nargs == 1 && !c.inEnv(SymID(head), env) {
		c.compileIn(e, env, false, carFast(args))
		e.emit(opCadr)
		return
	}

	c.compileIn(e, env, false, head)
	for p := args; IsCons(p); p = cdrFast(p) {
		c.compileIn(e, env, false, carFast(p))
	}
	if tail {
		e.emitU8(opTCall, uint8(nargs))
	} else {
		e.emitU8(opCall, uint8(nargs))
	}
}

func (c *compiler) compileBuiltinApp(e *emitter, env [][]uint32, b *Builtin, args Value, nargs int) bool {
	op := b.opcode
	switch op {
	case opAddN:
		switch nargs {
		case 0:
			e.emit(opLoad0)
			return true
		case 1:
			c.compileIn(e, env, false, carFast(args))
			return true
		case 2:
			c.compileIn(e, env, false, carFast(args))
			c.compileIn(e, env, false, cadr(args))
			e.emit(opAdd2)
			return true
		}
	case opSubN:
		if nargs == 1 {
			c.compileIn(e, env, false, carFast(args))
			e.emit(opNeg)
			return true
		}
		if nargs == 2 {
			c.compileIn(e, env, false, carFast(args))
			c.compileIn(e, env, false, cadr(args))
			e.emit(opSub2)
			return true
		}
	case opMulN:
		switch nargs {
		case 0:
			e.emit(opLoad1)
			return true
		case 1:
			c.compileIn(e, env, false, carFast(args))
			return true
		case 2:
			c.compileIn(e, env, false, carFast(args))
			c.compileIn(e, env, false, cadr(args))
			e.emit(opMulN)
			e.emit(2)
			return true
		}
	}
	if b.nargs >= 0 && nargs != b.nargs {
		raise(symArgError, "%s: expects %d arguments, got %d", b.name, b.nargs, nargs)
	}
	for p := args; IsCons(p); p = cdrFast(p) {
		c.compileIn(e, env, false, carFast(p))
	}
	e.emit(op)
	if op == opAddN || op == opSubN || op == opMulN || op == opDivN ||
		op == opListN || op == opVectorN || op == opApply || op == opTApply {
		e.emit(uint8(nargs))
	}
	return true
}

func (c *compiler) selfNameMatches(x Value) bool {
	if !c.curFn.hasSelf {
		return false
	}
	return SymID(x) == c.curFn.selfID
}

func (c *compiler) inEnv(id uint32, env [][]uint32) bool {
	for _, frame := range env {
		for _, s := range frame {
			if s == id {
				return true
			}
		}
	}
	return false
}

// --- quasiquote ---

func (c *compiler) expandQQ(x Value) Value {
	g := c.g
	if !IsCons(x) {
		if IsSymbol(x) || IsNumber(x) || IsString(x) || IsChar(x) || IsBool(x) || x == Nil {
			return g.list2(SymbolValue(symQuote), x)
		}
		return g.list2(SymbolValue(symQuote), x)
	}
	head := carFast(x)
	if IsSymbol(head) && SymID(head) == symUnquote {
		return cadr(x)
	}
	// list: handle splicing
	var parts []Value
	p := x
	for IsCons(p) {
		el := carFast(p)
		if IsCons(el) && carFast(el) == SymbolValue(symUnquoteS) {
			parts = append(parts, cadr(el))
		} else {
			parts = append(parts, g.list2(Symbol("list"), c.expandQQ(el)))
		}
		p = cdrFast(p)
	}
	if p != Nil {
		parts = append(parts, g.list2(SymbolValue(symQuote), p))
	}
	if len(parts) == 0 {
		return g.list2(SymbolValue(symQuote), Nil)
	}
	res := Symbol("append")
	for i := len(parts) - 1; i >= 0; i-- {
		res = g.Cons(parts[i], res)
	}
	return res
}

// --- list helpers (compile-time) ---

func cadr(x Value) Value {
	if !IsCons(x) {
		return Nil
	}
	d := cdrFast(x)
	if !IsCons(d) {
		return Nil
	}
	return carFast(d)
}

func caddr(x Value) Value  { return cadr(cdrOrNil(x)) }
func cadddr(x Value) Value { return cadr(cdrOrNil(cdrOrNil(x))) }
func cddr(x Value) Value   { return cdrOrNil(cdrOrNil(x)) }
func cdddr(x Value) Value  { return cdrOrNil(cdrOrNil(cdrOrNil(x))) }

func cdrOrNil(x Value) Value {
	if IsCons(x) {
		return cdrFast(x)
	}
	return Nil
}

func listLen(x Value) int {
	n := 0
	for IsCons(x) {
		n++
		x = cdrFast(x)
	}
	return n
}

func (g *Interp) list2(a, b Value) Value { return g.Cons(a, g.Cons(b, Nil)) }

func (g *Interp) list3(a, b, cc Value) Value {
	return g.Cons(a, g.Cons(b, g.Cons(cc, Nil)))
}

func (g *Interp) list4(a, b, cc, d Value) Value {
	return g.Cons(a, g.Cons(b, g.Cons(cc, g.Cons(d, Nil))))
}

func (g *Interp) cons2(a Value, rest Value) Value { return g.Cons(a, rest) }
func (g *Interp) cons3(a, b Value, rest Value) Value {
	return g.Cons(a, g.Cons(b, rest))
}

func (g *Interp) reverseList(l Value) Value {
	r := Nil
	for IsCons(l) {
		r = g.Cons(carFast(l), r)
		l = cdrFast(l)
	}
	return r
}

func (g *Interp) reverseListFromSlice(values []Value) Value {
	result := Nil
	for i := len(values) - 1; i >= 0; i-- {
		result = g.Cons(values[i], result)
	}
	return result
}

var _ = fmt.Sprintf
