package golisp

import (
	"fmt"
	"math"
	"strings"
)

// ---------------------------------------------------------------------------
// VM design
//
// Value stack layout for a frame:
//
//	base+0..nargs-1 : arguments
//	base+nargs ...  : temporaries (operand stack)
//
// Frame record (parallel arrays):
//
//	fn     - running function (with upvals)
//	base   - stack index of arg0
//	retPC  - pc in caller to resume at (or -1 for top)
//	nCaps  - number of frame slots captured by closures (cells in capCells)
//
// Captured locals are stored in capCells (a slice of *FrameCell) that is a
// GC root and shared with closures. On call, after fixArgs, the callee's
// captured slots (fn.capSlots) are initialized: capCells[i] = newCell(stack
// [base+slot]). SETA to a captured slot writes the cell; LOADA reads it.
// On return, cells stay alive via closures; uncaptured slots die with frame.
//
// VM run loop is recursive: apply() pushes a frame, exec() runs until ret,
// then pops. Tail calls reuse the current frame's stack region.
// ---------------------------------------------------------------------------

type vmFrame struct {
	fn       *Function
	base     int
	capCells []*FrameCell // indexed by capIdx (fn.capBySlot inverse)
	cellBase int          // index into Interp.cellPool where our cells start
}

type LispError2 = LispError

// unspecified marks an optional argument that was not supplied.
var unspecified = Value(qNaN | 7)

func (g *Interp) apply(fn *Function, args []Value) Value {
	g.maybeGC()
	if g.sp+len(args)+64 >= len(g.stack) {
		g.growStack(len(args) + 256)
	}
	base := g.sp
	copy(g.stack[base:], args)
	g.sp = base + len(args)
	result := g.call(fn, base, 0)
	g.sp = base
	return result
}

func (g *Interp) growStack(need int) {
	n := len(g.stack) * 2
	for n < len(g.stack)+need {
		n *= 2
	}
	ns := make([]Value, n)
	copy(ns, g.stack)
	g.stack = ns
}

// call runs fn with arguments already on the stack at base.
//
// The argument count is normalized first: variadic functions gather extras
// into a rest list, optional arguments are filled with the unspecified
// sentinel, and internal-define slots are initialized to nil. selfV is the
// closure value being called; it fills the function's self-name slot.
func (g *Interp) call(fn *Function, base int, selfV Value) Value {
	g.prepareFrame(fn, base, selfV)
	return g.execFrame(fn, base)
}

// prepareFrame normalizes the arguments sitting at stack[base:g.sp] for fn
// and pads the frame. Variadic extras are gathered into a rest list,
// optional arguments are filled with the unspecified sentinel, internal
// define slots become nil, and selfV fills the self-name slot. It leaves
// g.sp at the top of the prepared frame.
func (g *Interp) prepareFrame(fn *Function, base int, selfV Value) {
	given := g.sp - base
	want := fn.nargs
	if fn.variadic {
		fixed := want - 1
		if given < fixed {
			raise(symArgError, "%s: too few arguments (%d < %d)", fn.name, given, fixed)
		}
		var rest Value = Nil
		for i := given - 1; i >= fixed; i-- {
			rest = g.Cons(g.stack[base+i], rest)
		}
		g.sp = base + fixed
		g.push(rest)
	} else if fn.hasOpt {
		if given < fn.nreq || given > want {
			raise(symArgError, "%s: expects between %d and %d args, got %d", fn.name, fn.nreq, want, given)
		}
		for i := given; i < want; i++ {
			g.push(unspecified)
		}
	} else if given != want {
		raise(symArgError, "%s: expects %d args, got %d", fn.name, want, given)
	}
	for i := g.sp - base; i < fn.frameSize; i++ {
		g.push(Nil)
	}
	if fn.hasSelf {
		g.stack[base+fn.frameSize-1] = selfV
	}
}

// setupTailFrame reuses the current frame's stack region for a tail call.
// It moves the arguments for fn2 down to base and prepares the frame.
func (g *Interp) setupTailFrame(fn2 *Function, base int, args []Value, selfV Value) {
	g.sp = base
	copy(g.stack[base:], args)
	g.sp = base + len(args)
	g.prepareFrame(fn2, base, selfV)
}

// setupTailFrameStack reuses arguments already resident in the VM stack. It
// avoids the temporary []Value allocation used at Go boundaries.
func (g *Interp) setupTailFrameStack(fn2 *Function, base, argsBase, nargs int, selfV Value) {
	copy(g.stack[base:base+nargs], g.stack[argsBase:argsBase+nargs])
	g.sp = base + nargs
	g.prepareFrame(fn2, base, selfV)
}

func (g *Interp) execFrame(fn *Function, base int) (ret Value) {
	// set up captured cells
	var capCells []*FrameCell
	if len(fn.capSlots) > 0 {
		capCells = g.allocCells(len(fn.capSlots))
		for i, slot := range fn.capSlots {
			capCells[i].v = g.stack[base+slot]
		}
	}

	// defer: on LispError, unwind to nearest trycatch
	defer func() {
		if r := recover(); r != nil {
			if le, ok := r.(*LispError); ok {
				for len(g.tryStack) > 0 {
					tf := g.tryStack[len(g.tryStack)-1]
					g.tryStack = g.tryStack[:len(g.tryStack)-1]
					// unwind
					g.sp = tf.sp
					g.popCells(tf.cellTop)
					errV := g.errorValue(le)
					res := g.applyValue(tf.handler, []Value{errV})
					ret = res
					return
				}
			}
			panic(r)
		}
	}()

	v := g.run(fn, base, capCells)
	return v
}

// cell pool
func (g *Interp) allocCells(n int) []*FrameCell {
	out := make([]*FrameCell, n)
	for i := range out {
		out[i] = &FrameCell{}
	}
	g.cellPool = append(g.cellPool, out...)
	return out
}

func (g *Interp) popCells(to int) {
	// cells beyond `to` die (unless captured; GC finds them via closures)
	if to < len(g.cellPool) {
		g.cellPool = g.cellPool[:to]
	}
}

// run executes fn at frame base with pre-initialized capCells.
func (g *Interp) run(fn *Function, base int, capCells []*FrameCell) Value {
	stack := g.stack
	code := fn.code
	consts := fn.consts
	pc := 0
	frameSize := fn.frameSize
	if fn.hasSelf {
		frameSize++ // self-name slot sits after frame vars
	}
	sp := base + frameSize
	upvals := fn.upvals

	for {
		g.checkContext()
		op := code[pc]
		pc++
		switch op {
		case opLoadT:
			stack[sp] = True
			sp++
		case opLoadF:
			stack[sp] = False
			sp++
		case opLoadNil:
			stack[sp] = Nil
			sp++
		case opLoad0:
			stack[sp] = fixnum0
			sp++
		case opLoad1:
			stack[sp] = fixnum1
			sp++
		case opLoadI8:
			stack[sp] = FixnumValue(int64(int8(code[pc])))
			pc++
			sp++
		case opLoadV:
			stack[sp] = consts[uint16(code[pc])|uint16(code[pc+1])<<8]
			pc += 2
			sp++
		case opLoadG:
			id := decU32(code, pc)
			pc += 4
			g.ensureSymbolState(id)
			v := g.globals[id]
			if v == 0 {
				g.sp = sp
				raise(symUnboundError, "variable %s has no value", symNameByID(id))
			}
			stack[sp] = v
			sp++
		case opLoadA0:
			stack[sp] = stack[base]
			sp++
		case opLoadA1:
			stack[sp] = stack[base+1]
			sp++
		case opLoadA:
			slot := int(uint16(code[pc]) | uint16(code[pc+1])<<8)
			pc += 2
			if ci, ok := fn.capBySlot[slot]; ok {
				stack[sp] = capCells[ci].v
			} else {
				stack[sp] = stack[base+slot]
			}
			sp++
		case opLoadC00:
			stack[sp] = upvals[0].v
			sp++
		case opLoadC01:
			stack[sp] = upvals[1].v
			sp++
		case opLoadC:
			lev := code[pc]
			slot := int(code[pc+1])
			pc += 2
			if lev == 1 {
				// this frame's own captured slot
				stack[sp] = capCells[slot].v
			} else {
				stack[sp] = upvals[slot].v
			}
			sp++
		case opSetA:
			slot := int(uint16(code[pc]) | uint16(code[pc+1])<<8)
			pc += 2
			if ci, ok := fn.capBySlot[slot]; ok {
				capCells[ci].v = stack[sp-1]
			} else {
				stack[base+slot] = stack[sp-1]
			}
		case opSetC:
			lev := code[pc]
			slot := int(code[pc+1])
			pc += 2
			if lev == 1 {
				capCells[slot].v = stack[sp-1]
			} else {
				upvals[slot].v = stack[sp-1]
			}
		case opSetG:
			id := decU32(code, pc)
			pc += 4
			g.setGlobal(id, stack[sp-1])
		case opDup:
			stack[sp] = stack[sp-1]
			sp++
		case opPop:
			if sp <= base {
				raise(symError, "vm stack underflow at pop")
			}
			sp--
		case opJmp:
			rel := decI32(code, pc)
			pc += 4 + rel
		case opBrNumLt, opBrNumEq:
			rel := decI32(code, pc)
			pc += 4
			if sp < base+2 {
				raise(symError, "vm stack underflow at numeric branch")
			}
			b := stack[sp-1]
			a := stack[sp-2]
			sp -= 2
			condition := false
			if op == opBrNumEq {
				condition = numEq(a, b)
			} else if IsFixnum(a) && IsFixnum(b) {
				condition = Fixnum(a) < Fixnum(b)
			} else {
				condition = numLt(a, b)
			}
			if !condition {
				pc += rel
			}

		case opBrf:
			rel := decI32(code, pc)
			pc += 4
			if sp <= base {
				raise(symError, "vm stack underflow at brf")
			}
			sp--
			if stack[sp] == False {
				pc += rel
			}
		case opBrt:
			rel := decI32(code, pc)
			pc += 4
			if sp <= base {
				raise(symError, "vm stack underflow at brt")
			}
			sp--
			if stack[sp] != False {
				pc += rel
			}
		case opRet:
			g.sp = sp
			return stack[sp-1]

		// --- arithmetic ---
		case opAdd2:
			b := stack[sp-1]
			a := stack[sp-2]
			sp--
			if a&tagMask == tagFixnum && b&tagMask == tagFixnum {
				r := Fixnum(a) + Fixnum(b)
				if r >= fixnumMin && r <= fixnumMax {
					stack[sp-1] = FixnumValue(r)
				} else {
					stack[sp-1] = DoubleValue(float64(Fixnum(a)) + float64(Fixnum(b)))
				}
			} else {
				stack[sp-1] = numAdd(a, b)
			}
		case opSub2:
			b := stack[sp-1]
			a := stack[sp-2]
			sp--
			if a&tagMask == tagFixnum && b&tagMask == tagFixnum {
				r := Fixnum(a) - Fixnum(b)
				if r >= fixnumMin && r <= fixnumMax {
					stack[sp-1] = FixnumValue(r)
				} else {
					stack[sp-1] = DoubleValue(float64(Fixnum(a)) - float64(Fixnum(b)))
				}
			} else {
				stack[sp-1] = numSub(a, b)
			}
		case opNeg:
			a := stack[sp-1]
			if a&tagMask == tagFixnum {
				stack[sp-1] = FixnumValue(-Fixnum(a))
			} else {
				stack[sp-1] = DoubleValue(-toFloat(a))
			}
		case opAddN, opSubN, opMulN, opDivN:
			n := int(code[pc])
			pc++
			args := stack[sp-n : sp]
			sp -= n
			var r Value
			switch op {
			case opAddN:
				r = fixnum0
				for _, a := range args {
					r = numAdd(r, a)
				}
			case opSubN:
				if n == 1 {
					r = numSub(fixnum0, args[0])
				} else {
					r = args[0]
					for _, a := range args[1:] {
						r = numSub(r, a)
					}
				}
			case opMulN:
				r = fixnum1
				for _, a := range args {
					r = numMul(r, a)
				}
			case opDivN:
				if n == 1 {
					r = numDiv(fixnum1, args[0])
				} else {
					r = args[0]
					for _, a := range args[1:] {
						r = numDiv(r, a)
					}
				}
			}
			stack[sp] = r
			sp++
		case opIDiv:
			b := stack[sp-1]
			a := stack[sp-2]
			sp--
			stack[sp-1] = numIDiv(a, b)
		case opNumEq:
			b := stack[sp-1]
			a := stack[sp-2]
			sp--
			stack[sp-1] = BoolValue(numEq(a, b))
		case opNumLt:
			b := stack[sp-1]
			a := stack[sp-2]
			sp--
			if a&tagMask == tagFixnum && b&tagMask == tagFixnum {
				stack[sp-1] = BoolValue(Fixnum(a) < Fixnum(b))
			} else {
				stack[sp-1] = BoolValue(numLt(a, b))
			}
		case opCompare:
			b := stack[sp-1]
			a := stack[sp-2]
			sp--
			stack[sp-1] = FixnumValue(int64(Compare(a, b)))

		// --- predicates ---
		case opEq:
			b := stack[sp-1]
			a := stack[sp-2]
			sp--
			stack[sp-1] = BoolValue(a == b || (IsNumber(a) && IsNumber(b) && numEq(a, b)))
		case opEqv:
			b := stack[sp-1]
			a := stack[sp-2]
			sp--
			stack[sp-1] = BoolValue(Eqv(a, b))
		case opEqual:
			b := stack[sp-1]
			a := stack[sp-2]
			sp--
			stack[sp-1] = BoolValue(Equal(a, b))
		case opAtomp:
			stack[sp-1] = BoolValue(!IsCons(stack[sp-1]))
		case opNot:
			stack[sp-1] = BoolValue(stack[sp-1] == False)
		case opNullp:
			stack[sp-1] = BoolValue(stack[sp-1] == Nil)
		case opBooleanp:
			stack[sp-1] = BoolValue(IsBool(stack[sp-1]))
		case opSymbolp:
			stack[sp-1] = BoolValue(IsSymbol(stack[sp-1]))
		case opNumberp:
			stack[sp-1] = BoolValue(IsNumber(stack[sp-1]))
		case opBoundp:
			v := stack[sp-1]
			if !IsSymbol(v) {
				stack[sp-1] = False
			} else {
				_, bound := g.symbolValue(SymID(v))
				stack[sp-1] = BoolValue(bound)
			}
		case opPairp:
			stack[sp-1] = BoolValue(IsCons(stack[sp-1]))
		case opBuiltinp:
			stack[sp-1] = BoolValue(IsBoxed(stack[sp-1]) && AsBuiltin(stack[sp-1]) != nil)
		case opVectorp:
			stack[sp-1] = BoolValue(IsVector(stack[sp-1]))
		case opFixnump:
			stack[sp-1] = BoolValue(IsFixnum(stack[sp-1]))
		case opFunctionp:
			v := stack[sp-1]
			stack[sp-1] = BoolValue(IsBoxed(v) && (AsFunction(v) != nil || AsBuiltin(v) != nil))

		// --- lists ---
		case opCons:
			d := stack[sp-1]
			a := stack[sp-2]
			sp -= 2
			stack[sp] = g.Cons(a, d)
			sp++
		case opListN:
			n := int(code[pc])
			pc++
			l := Nil
			for i := n - 1; i >= 0; i-- {
				l = g.Cons(stack[sp-n+i], l)
			}
			sp -= n
			stack[sp] = l
			sp++
		case opCar:
			v := stack[sp-1]
			if IsCons(v) {
				stack[sp-1] = carFast(v)
			} else if v == Nil {
				stack[sp-1] = Nil
			} else {
				g.sp = sp
				raise(symTypeError, "car: expected cons, got %s", typeName(v))
			}
		case opCdr:
			v := stack[sp-1]
			if IsCons(v) {
				stack[sp-1] = cdrFast(v)
			} else if v == Nil {
				stack[sp-1] = Nil
			} else {
				g.sp = sp
				raise(symTypeError, "cdr: expected cons, got %s", typeName(v))
			}
		case opCadr:
			v := stack[sp-1]
			if IsCons(v) {
				d := cdrFast(v)
				if IsCons(d) {
					stack[sp-1] = carFast(d)
				} else {
					stack[sp-1] = Nil
				}
			} else {
				stack[sp-1] = Nil
			}
		case opSetCar:
			x := stack[sp-1]
			v := stack[sp-2]
			sp -= 2
			if !IsCons(v) {
				g.sp = sp
				raise(symTypeError, "set-car!: expected cons")
			}
			SetCar(v, x)
			stack[sp] = x
			sp++
		case opSetCdr:
			x := stack[sp-1]
			v := stack[sp-2]
			sp -= 2
			if !IsCons(v) {
				g.sp = sp
				raise(symTypeError, "set-cdr!: expected cons")
			}
			SetCdr(v, x)
			stack[sp] = x
			sp++

		// --- vectors ---
		case opVectorN:
			n := int(code[pc])
			pc++
			vec := g.NewVector(n, Nil)
			d := vectorData(vec)
			copy(d, stack[sp-n:sp])
			sp -= n
			stack[sp] = vec
			sp++
		case opAref:
			idx := stack[sp-1]
			v := stack[sp-2]
			sp--
			i := toInt(idx, "aref")
			switch {
			case IsVector(v):
				stack[sp-1] = VectorRef(v, int(i))
			case IsString(v):
				s := GoString(v)
				if i < 0 || int(i) >= len(s) {
					raise(symBoundsError, "aref: index out of bounds")
				}
				stack[sp-1] = CharValue(rune(s[i]))
			default:
				raise(symTypeError, "aref: expected vector or string")
			}
		case opAset:
			x := stack[sp-1]
			idx := stack[sp-2]
			v := stack[sp-3]
			sp -= 3
			i := toInt(idx, "aset!")
			if IsVector(v) {
				VectorSet(v, int(i), x)
			} else {
				g.sp = sp
				raise(symTypeError, "aset!: expected vector")
			}
			stack[sp] = x
			sp++

		// --- calls ---

		case opSelfCall, opSelfTCall:
			n := int(code[pc])
			pc++
			callBase := sp - n
			selfV := Nil
			if fn.hasSelf && fn.frameSize > 0 {
				selfV = stack[base+fn.frameSize-1]
			}
			if op == opSelfTCall {
				// Most recursive loops have a fixed frame with only the
				// self slot beyond the arguments. Reuse it directly and
				// skip the generic optional/variadic/capture setup.
				if !fn.variadic && !fn.hasOpt && len(fn.capSlots) == 0 &&
					fn.hasSelf && fn.frameSize == fn.nargs+1 && n == fn.nargs {
					copy(stack[base:base+n], stack[callBase:sp])
					sp = base + fn.frameSize
					g.sp = sp
					pc = 0
					continue
				}
				g.setupTailFrameStack(fn, base, callBase, n, selfV)
				stack = g.stack
				sp = g.sp
				capCells = nil
				if len(fn.capSlots) > 0 {
					capCells = g.allocCells(len(fn.capSlots))
					for i, slot := range fn.capSlots {
						capCells[i].v = stack[base+slot]
					}
				}
				pc = 0
				continue
			}
			g.sp = sp
			g.stack = stack
			g.prepareFrame(fn, callBase, selfV)
			r := g.execFrame(fn, callBase)
			stack = g.stack
			sp = callBase
			stack[sp] = r
			sp++
			g.sp = sp

		case opKnownCall, opKnownTCall:
			idx := int(uint16(code[pc]) | uint16(code[pc+1])<<8)
			n := int(code[pc+2])
			pc += 3
			callee := AsFunction(consts[idx])
			if callee == nil {
				raise(symError, "known-call constant is not a Lisp function")
			}
			callBase := sp - n
			if op == opKnownTCall {
				g.setupTailFrameStack(callee, base, callBase, n, consts[idx])
				fn = callee
				code = fn.code
				consts = fn.consts
				upvals = fn.upvals
				stack = g.stack
				sp = g.sp
				capCells = nil
				if len(fn.capSlots) > 0 {
					capCells = g.allocCells(len(fn.capSlots))
					for i, slot := range fn.capSlots {
						capCells[i].v = stack[base+slot]
					}
				}
				pc = 0
				continue
			}
			g.sp = sp
			g.stack = stack
			g.prepareFrame(callee, callBase, consts[idx])
			r := g.execFrame(callee, callBase)
			stack = g.stack
			sp = callBase
			stack[sp] = r
			sp++
			g.sp = sp

		case opCall:
			n := int(code[pc])
			pc++
			f := stack[sp-n-1]
			callBase := sp - n - 1
			g.sp = callBase
			g.stack = stack
			if IsBoxed(f) {
				if b := AsBuiltin(f); b != nil {
					r := b.f(g, stack[sp-n:sp])
					stack = g.stack
					sp = callBase
					stack[sp] = r
					sp++
					g.sp = sp
					continue
				}
				if goFn, name, ok := AsGoFunc(f); ok {
					r := callReflect(g, goFn, stack[sp-n:sp], name)
					stack = g.stack
					sp = callBase
					stack[sp] = r
					sp++
					g.sp = sp
					continue
				}
				if fn2 := AsFunction(f); fn2 != nil {
					copy(stack[callBase:], stack[sp-n:sp])
					g.sp = callBase + n
					// Lisp functions consume the caller's stack region directly.
					g.prepareFrame(fn2, callBase, f)
					r := g.execFrame(fn2, callBase)
					stack = g.stack
					sp = callBase
					stack[sp] = r
					sp++
					g.sp = sp
					continue
				}
			}
			args := append([]Value(nil), stack[sp-n:sp]...)
			r := g.applyValue(f, args)
			stack = g.stack
			sp = callBase
			stack[sp] = r
			sp++
			g.sp = sp
		case opTCall:
			n := int(code[pc])
			pc++
			f := stack[sp-n-1]
			args := make([]Value, n)
			copy(args, stack[sp-n:sp])
			sp = sp - n - 1
			g.sp = sp
			if IsBoxed(f) {
				if b := AsBuiltin(f); b != nil {
					stack[sp] = b.f(g, args)
					sp++
					continue
				}
				if goFn, name, ok := AsGoFunc(f); ok {
					stack[sp] = callReflect(g, goFn, args, name)
					sp++
					continue
				}
				if fn2 := AsFunction(f); fn2 != nil {
					// tail call: reuse our stack frame.
					// patch self-name slot before overwriting args
					if fn2.hasSelf {
						selfIdx := base + fn2.frameSize
						stack[selfIdx] = f
					}
					copy(stack[base:], args)
					sp = base + len(args)
					g.sp = sp
					g.stack = stack
					g.setupTailFrame(fn2, base, args, f)
					fn = fn2
					code = fn.code
					consts = fn.consts
					upvals = fn.upvals
					capCells = nil
					if len(fn.capSlots) > 0 {
						capCells = g.allocCells(len(fn.capSlots))
						for i, slot := range fn.capSlots {
							capCells[i].v = g.stack[base+slot]
						}
					}
					stack = g.stack
					sp = g.sp
					pc = 0
					continue
				}
			}
			raise(symTypeError, "attempt to call non-function: %s", typeName(f))

		case opClosure:
			ncap := int(code[pc])
			pc++
			proto := stack[sp-1]
			pfn := AsFunction(proto)
			if pfn == nil {
				g.sp = sp
				raise(symTypeError, "closure: not a function")
			}
			nf := &Function{
				code: pfn.code, consts: pfn.consts, name: pfn.name,
				nargs: pfn.nargs, nreq: pfn.nreq, variadic: pfn.variadic,
				hasOpt: pfn.hasOpt, hasSelf: pfn.hasSelf, frameSize: pfn.frameSize,
				capSlots: pfn.capSlots, capBySlot: pfn.capBySlot,
				capSrc: pfn.capSrc, capSymIDs: pfn.capSymIDs, capBySym: pfn.capBySym,
				selfRef: pfn.selfRef, selfID: pfn.selfID,
				upvals: make([]*FrameCell, ncap),
			}
			_ = pc // closure operands are embedded in the prototype (capSrc)
			for i := 0; i < ncap; i++ {
				src := pfn.capSrc[i]
				lev := int(src >> 16)
				slot := int(src & 0xffff)
				if lev <= 1 {
					// lev 1: parent frame's cell; lev 0 is retained for
					// compatibility with older prototypes.
					if capCells == nil {
						nf.upvals[i] = &FrameCell{v: stack[base+slot]}
						g.cellPool = append(g.cellPool, nf.upvals[i])
					} else if ci, ok := fn.capBySlot[slot]; ok {
						nf.upvals[i] = capCells[ci]
					} else {
						nf.upvals[i] = &FrameCell{v: stack[base+slot]}
						g.cellPool = append(g.cellPool, nf.upvals[i])
					}
				} else {
					// parent accesses via its own upvals; find by symID
					symID := pfn.capSymIDs[i]
					ci, ok := fn.capBySym[symID]
					if !ok {
						g.sp = sp
						raise(symError, "closure: parent has no upval for %s", symNameByID(symID))
					}
					nf.upvals[i] = upvals[ci]
				}
			}
			stack[sp-1] = g.box(boxFunction{nf})
			// patch our own self-name slot if we have one
			if fn.hasSelf {
				stack[base+fn.frameSize] = stack[sp-1]
				if ci, ok := fn.capBySlot[fn.frameSize]; ok && capCells != nil {
					capCells[ci].v = stack[sp-1]
				}
			}

		case opGoMember:
			prefixID := decU32(code, pc)
			member := consts[uint16(code[pc+4])|uint16(code[pc+5])<<8]
			fullID := decU32(code, pc+6)
			callable := code[pc+10] != 0
			pc += 11
			g.sp = sp
			v := g.GoMember(SymbolValue(prefixID), SymbolValue(fullID),
				GoString(member), callable)
			stack = g.stack
			sp = g.sp
			stack[sp] = v
			sp++
			g.sp = sp

		case opGoSet:
			prefixID := decU32(code, pc)
			member := consts[uint16(code[pc+4])|uint16(code[pc+5])<<8]
			pc += 6
			g.sp = sp
			fullName := SymName(SymbolValue(prefixID)) + "." + GoString(member)
			g.GoSet(SymbolValue(prefixID), SymbolValue(internString(fullName)),
				GoString(member), stack[sp-1])
			stack = g.stack
			sp = g.sp

		case opArgc, opVArgc:
			pc++ // arity checked in call
		case opOptArgs:
			pc += 2
		case opBrBound:
			slot := int(uint16(code[pc]) | uint16(code[pc+1])<<8)
			pc += 2
			rel := decI32(code, pc)
			pc += 4
			if stack[base+slot] == unspecified {
				// fall through to default init
				_ = rel
			} else {
				pc += rel
			}

		case opTryCatch:
			handler := stack[sp-1]
			body := stack[sp-2]
			sp -= 2
			g.sp = sp
			g.stack = stack
			tf := tryFrame{sp: sp, cellTop: len(g.cellPool), handler: handler}
			g.tryStack = append(g.tryStack, tf)
			var res Value
			func() {
				defer func() {
					if r := recover(); r != nil {
						if le, ok := r.(*LispError); ok {
							g.tryStack = g.tryStack[:len(g.tryStack)-1]
							g.sp = tf.sp
							g.popCells(tf.cellTop)
							res = g.applyValue(handler, []Value{g.errorValue(le)})
							return
						}
						panic(r)
					}
				}()
				res = g.applyValue(body, nil)
			}()
			g.tryStack = g.tryStack[:len(g.tryStack)-1]
			stack = g.stack
			sp = g.sp
			stack[sp] = res
			sp++

		case opFor:
			f := stack[sp-1]
			hi := toInt(stack[sp-2], "for")
			lo := toInt(stack[sp-3], "for")
			sp -= 3
			g.sp = sp
			g.stack = stack
			for i := lo; i < hi; i++ {
				g.applyValue(f, []Value{FixnumValue(i)})
			}
			stack = g.stack
			stack[sp] = Nil
			sp++

		case opApply:
			// The operand counts the source arguments, so the callee is one
			// of them: (apply f a list) leaves f, a and list on the stack.
			n := int(code[pc])
			pc++
			if n < 2 {
				raise(symArgError, "apply: expects a function and a list")
			}
			f := stack[sp-n]
			last := stack[sp-1]
			all := make([]Value, 0, n+8)
			all = append(all, stack[sp-n+1:sp-1]...)
			for IsCons(last) {
				all = append(all, carFast(last))
				last = cdrFast(last)
			}
			sp -= n
			g.sp = sp
			g.stack = stack
			r := g.applyValue(f, all)
			stack = g.stack
			g.sp = sp // applyValue runs callees, which move g.sp
			stack[sp] = r
			sp++
			g.sp = sp
		case opTApply:
			n := int(code[pc])
			pc++
			if n < 2 {
				raise(symArgError, "apply: expects a function and a list")
			}
			f := stack[sp-n]
			last := stack[sp-1]
			all := make([]Value, 0, n+8)
			all = append(all, stack[sp-n+1:sp-1]...)
			for IsCons(last) {
				all = append(all, carFast(last))
				last = cdrFast(last)
			}
			sp -= n
			g.sp = sp
			g.stack = stack
			if IsBoxed(f) {
				if fn2 := AsFunction(f); fn2 != nil {
					g.setupTailFrame(fn2, base, all, f)
					fn = fn2
					code = fn.code
					consts = fn.consts
					upvals = fn.upvals
					capCells = nil
					if len(fn.capSlots) > 0 {
						capCells = g.allocCells(len(fn.capSlots))
						for i, slot := range fn.capSlots {
							capCells[i].v = g.stack[base+slot]
						}
					}
					stack = g.stack
					sp = g.sp
					pc = 0
					continue
				}
			}
			r := g.applyValue(f, all)
			stack = g.stack
			g.sp = sp // applyValue runs callees, which move g.sp
			stack[sp] = r
			sp++
			g.sp = sp

		case opBrNe, opBrNN, opBrN:
			rel := decI32(code, pc)
			pc += 4
			v := stack[sp-1]
			var take bool
			switch op {
			case opBrNN:
				take = v != Nil
			case opBrN:
				take = v == Nil
			case opBrNe:
				take = false
			}
			sp--
			if take {
				pc += rel
			}

		default:
			g.sp = sp
			raise(symError, "unknown opcode %d at pc %d in %s", op, pc-1, fn.name)
		}
	}
}

func decU32(code []uint8, pc int) uint32 {
	return uint32(code[pc]) | uint32(code[pc+1])<<8 | uint32(code[pc+2])<<16 | uint32(code[pc+3])<<24
}

func decI32(code []uint8, pc int) int {
	return int(int32(uint32(code[pc]) | uint32(code[pc+1])<<8 | uint32(code[pc+2])<<16 | uint32(code[pc+3])<<24))
}

var (
	fixnum0 = FixnumValue(0)
	fixnum1 = FixnumValue(1)
)

// callValue calls any value f with n args at stack positions.
func (g *Interp) callValue(f Value, base, n int) Value {
	args := make([]Value, n)
	copy(args, g.stack[base:base+n])
	g.sp = base
	if IsBoxed(f) {
		if b := AsBuiltin(f); b != nil {
			return b.f(g, args)
		}
		if fn := AsFunction(f); fn != nil {
			base := g.sp
			copy(g.stack[base:], args)
			g.sp = base + len(args)
			return g.call(fn, base, f)
		}
	}
	raise(symTypeError, "attempt to call non-function: %s", typeName(f))
	return Nil
}

func (g *Interp) applyValue(f Value, args []Value) Value {
	if IsBoxed(f) {
		if goFn, name, ok := AsGoFunc(f); ok {
			return callReflect(g, goFn, args, name)
		}
		if b := AsBuiltin(f); b != nil {
			return b.f(g, args)
		}
		if fn := AsFunction(f); fn != nil {
			base := g.sp
			copy(g.stack[base:], args)
			g.sp = base + len(args)
			return g.call(fn, base, f)
		}
	}
	raise(symTypeError, "attempt to call non-function: %s", typeName(f))
	return Nil
}

// Dump disassembles a function for debugging.
func (g *Interp) Dump(fn *Function) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "fn %s nargs=%d frameSize=%d hasSelf=%v\n", fn.name, fn.nargs, fn.frameSize, fn.hasSelf)
	pc := 0
	for pc < len(fn.code) {
		op := fn.code[pc]
		name := "?"
		if int(op) < len(opNames) && opNames[op] != "" {
			name = opNames[op]
		}
		fmt.Fprintf(&sb, "  %4d %s", pc, name)
		pc++
		switch op {
		case opLoadV:
			fmt.Fprintf(&sb, " %d", int(uint16(fn.code[pc])|uint16(fn.code[pc+1])<<8))
			pc += 2
		case opLoadI8:
			fmt.Fprintf(&sb, " %d", int8(fn.code[pc]))
			pc++
		case opLoadG, opSetG:
			id := decU32(fn.code, pc)
			fmt.Fprintf(&sb, " %s", symNameByID(id))
			pc += 4
		case opLoadA, opSetA:
			fmt.Fprintf(&sb, " %d", int(uint16(fn.code[pc])|uint16(fn.code[pc+1])<<8))
			pc += 2
		case opLoadC, opSetC:
			fmt.Fprintf(&sb, " %d %d", fn.code[pc], fn.code[pc+1])
			pc += 2
		case opJmp, opBrf, opBrt:
			fmt.Fprintf(&sb, " %+d", decI32(fn.code, pc))
			pc += 4
		case opCall, opTCall, opAddN, opSubN, opMulN, opDivN, opListN, opVectorN, opApply, opTApply, opArgc, opVArgc:
			fmt.Fprintf(&sb, " %d", fn.code[pc])
			pc++
		case opClosure:
			fmt.Fprintf(&sb, " %d", fn.code[pc])
			pc++
		case opOptArgs:
			fmt.Fprintf(&sb, " %d %d", fn.code[pc], fn.code[pc+1])
			pc += 2
		case opBrBound:
			fmt.Fprintf(&sb, " %d %+d", int(uint16(fn.code[pc])|uint16(fn.code[pc+1])<<8), decI32(fn.code, pc+2))
			pc += 6
		}
		sb.WriteByte('\n')
	}
	for i, c := range fn.consts {
		fmt.Fprintf(&sb, "  const[%d] = %s\n", i, DisplayString(c))
		if child := AsFunction(c); child != nil {
			childDump := g.Dump(child)
			for _, line := range strings.Split(strings.TrimSuffix(childDump, "\n"), "\n") {
				fmt.Fprintf(&sb, "    %s\n", line)
			}
		}
	}
	return sb.String()
}

func (g *Interp) errorValue(le *LispError) Value {
	kind := SymbolValue(le.kind)
	msg := g.NewString(le.msg)
	return g.Cons(kind, g.Cons(msg, Nil))
}

var (
	_ = fmt.Sprintf
	_ = math.Pi
)
