package interp

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Core builtins. These are the only primitives; everything else is defined
// in Lisp (system.lsp equivalent, inlined in Go strings below).

func (g *Interp) installCore() {
	defs := []struct {
		name string
		op   uint8
		narg int
		f    func(g *Interp, args []Value) Value
	}{
		// arithmetic
		{"+", opAddN, -1, bAdd},
		{"-", opSubN, -2, bSub},
		{"*", opMulN, -1, bMul},
		{"/", opDivN, -2, bDiv},
		{"div0", opIDiv, 2, bIDiv},
		{"=", opNumEq, 2, bNumEq},
		{"<", opNumLt, 2, bNumLt},
		{"compare", opCompare, 2, bCompare},
		{"mod", 0, 2, bMod},
		{"abs", 0, 1, bAbs},
		{"truncate", 0, 1, bTruncate},

		// predicates
		{"eq?", opEq, 2, bEq},
		{"eqv?", opEqv, 2, bEqv},
		{"equal?", opEqual, 2, bEqual},
		{"atom?", opAtomp, 1, bAtomp},
		{"not", opNot, 1, bNot},
		{"null?", opNullp, 1, bNullp},
		{"boolean?", opBooleanp, 1, bBooleanp},
		{"symbol?", opSymbolp, 1, bSymbolp},
		{"number?", opNumberp, 1, bNumberp},
		{"bound?", opBoundp, 1, bBoundp},
		{"pair?", opPairp, 1, bPairp},
		{"builtin?", opBuiltinp, 1, bBuiltinp},
		{"vector?", opVectorp, 1, bVectorp},
		{"fixnum?", opFixnump, 1, bFixnump},
		{"function?", opFunctionp, 1, bFunctionp},
		{"cons?", opPairp, 1, bPairp},
		{"list?", 0, 1, bListp},
		{"string?", 0, 1, bStringp},
		{"char?", 0, 1, bCharp},
		{"integer?", 0, 1, bIntegerp},
		{"procedure?", opFunctionp, 1, bFunctionp},

		// lists
		{"cons", opCons, 2, bCons},
		{"list", opListN, -1, bList},
		{"car", opCar, 1, bCar},
		{"cdr", opCdr, 1, bCdr},
		{"set-car!", opSetCar, 2, bSetCar},
		{"set-cdr!", opSetCdr, 2, bSetCdr},

		// vectors
		{"vector", opVectorN, -1, bVector},
		{"aref", opAref, 2, bAref},
		{"aset!", opAset, 3, bAset},
		{"vector.alloc", 0, 1, bVectorAlloc},
		{"length", 0, 1, bLength},

		// apply
		{"apply", opApply, -2, bApply},

		// tables
		{"table", 0, -1, bTable},
		{"get", 0, 2, bGet},
		{"put!", 0, 3, bPut},
		{"del!", 0, 2, bDel},
		{"has?", 0, 2, bHas},

		// strings
		{"string", 0, -1, bString},
		{"string.length", 0, 1, bStringLength},
		{"string.sub", 0, 3, bStringSub},
		{"string.find", 0, -2, bStringFind},
		{"string.char", 0, 2, bStringChar},
		{"string.inc", 0, 3, bStringInc},
		{"string.dec", 0, 3, bStringDec},
		{"symbol", 0, 1, bSymbol},
		{"gensym", 0, 0, bGensym},

		// io
		{"princ", 0, -1, bPrinc},
		{"print", 0, -1, bPrint},
		{"newline", 0, 0, bNewline},
		{"io.write", 0, -1, bIOWrite},
		{"write", 0, -1, bWrite},
		{"write-line", 0, -1, bWriteLine},
		{"flush", 0, 1, bFlush},
		{"close", 0, 1, bClose},
		{"read-line", 0, 1, bReadLine},
		{"read-all", 0, 1, bReadAll},
		{"read-n", 0, 2, bReadN},
		{"stream.name", 0, 1, bStreamName},

		// files
		{"open", 0, -1, bOpen},
		{"read-file", 0, 1, bReadFile},
		{"write-file", 0, 2, bWriteFile},
		{"append-file", 0, 2, bAppendFile},
		{"file-exists?", 0, 1, bFileExists},
		{"delete-file", 0, 1, bDeleteFile},

		// tcp
		{"tcp-connect", 0, 2, bTCPConnect},
		{"tcp-listen", 0, 2, bTCPListen},
		{"tcp-accept", 0, 1, bTCPAccept},
		{"listener-port", 0, 1, bListenerPort},
		{"listener-addr", 0, 1, bListenerAddr},

		// async
		{"par", 0, 1, bPar},
		{"wait", 0, 1, bWait},

		// misc
		{"error", 0, -1, bError},
		{"raise", 0, 1, bRaise},
		{"time", 0, 1, bTime},
		{"time.now", 0, 0, bTimeNow},
		{"rand", 0, -1, bRand},
		{"eval", 0, 1, bEval},
		{"load", 0, 1, bLoad},
		{"exit", 0, -1, bExit},
		{"void", 0, 0, bVoid},
		{"top-level-value", 0, 1, bTopLevelValue},
		{"set-top-level-value!", 0, 2, bSetTopLevelValue},
		{"constant?", 0, 1, bConstantp},
		{"logand", 0, 2, bLogand},
		{"ash", 0, 2, bAsh},
		{"number->string", 0, -1, bNumberToString},
		{"eof-object", 0, 0, bEofObject},
		{"eof-object?", 0, 1, bEofObjectP},
		{"go.call", 0, -2, bGoCall},
		{"go.get", 0, 2, bGoGet},
		{"go.set!", 0, 3, bGoSet},
		{"go.apply", 0, -1, bGoCallable},

		// modules. import/import* are macros; these are the primitives.
		{"import", 0, 1, bImport},
		{"module-import*", 0, -1, bImportStar},
		{"module-export", 0, -1, bExport},
	}
	for _, d := range defs {
		b := &Builtin{name: d.name, f: d.f, opcode: d.op, nargs: d.narg}
		v := g.box(boxBuiltin{b})
		id := internString(d.name)
		g.ensureSymbolState(id)
		g.globals[id] = v
		g.builtins[id] = b
		g.constants[id] = true
	}
}

// --- arithmetic ---

func bAdd(g *Interp, args []Value) Value {
	r := fixnum0
	for _, a := range args {
		r = numAdd(r, a)
	}
	return r
}

func bSub(g *Interp, args []Value) Value {
	if len(args) == 0 {
		raise(symArgError, "-: too few arguments")
	}
	if len(args) == 1 {
		return numSub(fixnum0, args[0])
	}
	r := args[0]
	for _, a := range args[1:] {
		r = numSub(r, a)
	}
	return r
}

func bMul(g *Interp, args []Value) Value {
	r := fixnum1
	for _, a := range args {
		r = numMul(r, a)
	}
	return r
}

func bDiv(g *Interp, args []Value) Value {
	if len(args) == 0 {
		raise(symArgError, "/: too few arguments")
	}
	if len(args) == 1 {
		return numDiv(fixnum1, args[0])
	}
	r := args[0]
	for _, a := range args[1:] {
		r = numDiv(r, a)
	}
	return r
}

func bIDiv(g *Interp, args []Value) Value { return numIDiv(args[0], args[1]) }
func bNumEq(g *Interp, args []Value) Value {
	return BoolValue(numEq(args[0], args[1]))
}
func bNumLt(g *Interp, args []Value) Value {
	return BoolValue(numLt(args[0], args[1]))
}
func bCompare(g *Interp, args []Value) Value {
	return FixnumValue(int64(Compare(args[0], args[1])))
}
func bMod(g *Interp, args []Value) Value {
	a, b := toInt(args[0], "mod"), toInt(args[1], "mod")
	if b == 0 {
		raise(symDivideError, "mod: division by zero")
	}
	return FixnumValue(a % b)
}
func bAbs(g *Interp, args []Value) Value {
	if IsFixnum(args[0]) {
		n := Fixnum(args[0])
		if n < 0 {
			return FixnumValue(-n)
		}
		return args[0]
	}
	return DoubleValue(math.Abs(toFloat(args[0])))
}
func bTruncate(g *Interp, args []Value) Value {
	if IsFixnum(args[0]) {
		return args[0]
	}
	return DoubleValue(math.Trunc(toFloat(args[0])))
}

// --- predicates ---

func bEq(g *Interp, args []Value) Value {
	a, b := args[0], args[1]
	return BoolValue(a == b || (IsNumber(a) && IsNumber(b) && numEq(a, b)))
}
func bEqv(g *Interp, args []Value) Value { return BoolValue(Eqv(args[0], args[1])) }
func bEqual(g *Interp, args []Value) Value {
	return BoolValue(Equal(args[0], args[1]))
}
func bAtomp(g *Interp, args []Value) Value    { return BoolValue(!IsCons(args[0])) }
func bNot(g *Interp, args []Value) Value      { return BoolValue(args[0] == False) }
func bNullp(g *Interp, args []Value) Value    { return BoolValue(args[0] == Nil) }
func bBooleanp(g *Interp, args []Value) Value { return BoolValue(IsBool(args[0])) }
func bSymbolp(g *Interp, args []Value) Value  { return BoolValue(IsSymbol(args[0])) }
func bNumberp(g *Interp, args []Value) Value  { return BoolValue(IsNumber(args[0])) }
func bBoundp(g *Interp, args []Value) Value {
	v := args[0]
	if !IsSymbol(v) {
		return False
	}
	_, ok := g.dynLookup(SymID(v))
	return BoolValue(ok)
}
func bPairp(g *Interp, args []Value) Value { return BoolValue(IsCons(args[0])) }
func bBuiltinp(g *Interp, args []Value) Value {
	return BoolValue(IsBoxed(args[0]) && AsBuiltin(args[0]) != nil)
}
func bVectorp(g *Interp, args []Value) Value { return BoolValue(IsVector(args[0])) }
func bFixnump(g *Interp, args []Value) Value { return BoolValue(IsFixnum(args[0])) }
func bFunctionp(g *Interp, args []Value) Value {
	v := args[0]
	return BoolValue(IsBoxed(v) && (AsFunction(v) != nil || AsBuiltin(v) != nil))
}
func bListp(g *Interp, args []Value) Value {
	v := args[0]
	for {
		if v == Nil {
			return True
		}
		if !IsCons(v) {
			return False
		}
		v = cdrFast(v)
	}
}
func bStringp(g *Interp, args []Value) Value  { return BoolValue(IsString(args[0])) }
func bCharp(g *Interp, args []Value) Value    { return BoolValue(IsChar(args[0])) }
func bIntegerp(g *Interp, args []Value) Value { return BoolValue(IsFixnum(args[0])) }

// --- lists ---

func bCons(g *Interp, args []Value) Value { return g.Cons(args[0], args[1]) }
func bList(g *Interp, args []Value) Value {
	l := Nil
	for i := len(args) - 1; i >= 0; i-- {
		l = g.Cons(args[i], l)
	}
	return l
}
func bCar(g *Interp, args []Value) Value {
	v := args[0]
	if IsCons(v) {
		return carFast(v)
	}
	if v == Nil {
		return Nil
	}
	raise(symTypeError, "car: expected cons")
	return Nil
}
func bCdr(g *Interp, args []Value) Value {
	v := args[0]
	if IsCons(v) {
		return cdrFast(v)
	}
	if v == Nil {
		return Nil
	}
	raise(symTypeError, "cdr: expected cons")
	return Nil
}
func bSetCar(g *Interp, args []Value) Value {
	SetCar(args[0], args[1])
	return args[1]
}
func bSetCdr(g *Interp, args []Value) Value {
	SetCdr(args[0], args[1])
	return args[1]
}

// --- vectors ---

func bVector(g *Interp, args []Value) Value {
	v := g.NewVector(len(args), Nil)
	copy(vectorData(v), args)
	return v
}
func bAref(g *Interp, args []Value) Value {
	v, idx := args[0], toInt(args[1], "aref")
	if IsVector(v) {
		return VectorRef(v, int(idx))
	}
	if IsString(v) {
		s := GoString(v)
		if idx < 0 || int(idx) >= len(s) {
			raise(symBoundsError, "aref: index out of bounds")
		}
		return CharValue(rune(s[idx]))
	}
	raise(symTypeError, "aref: expected vector or string")
	return Nil
}
func bAset(g *Interp, args []Value) Value {
	v, idx, x := args[0], toInt(args[1], "aset!"), args[2]
	if IsVector(v) {
		VectorSet(v, int(idx), x)
		return x
	}
	raise(symTypeError, "aset!: expected vector")
	return Nil
}
func bVectorAlloc(g *Interp, args []Value) Value {
	return g.NewVector(int(toInt(args[0], "vector.alloc")), Nil)
}
func bLength(g *Interp, args []Value) Value {
	v := args[0]
	switch {
	case IsVector(v):
		return FixnumValue(int64(VectorLen(v)))
	case IsString(v):
		return FixnumValue(int64(len(GoString(v))))
	case v == Nil:
		return fixnum0
	case IsCons(v):
		n := 0
		for IsCons(v) {
			n++
			v = cdrFast(v)
		}
		return FixnumValue(int64(n))
	case IsBoxed(v):
		if t := AsTable(v); t != nil {
			return FixnumValue(int64(t.Count()))
		}
	}
	raise(symTypeError, "length: expected sequence")
	return Nil
}

func bApply(g *Interp, args []Value) Value {
	if len(args) < 1 {
		raise(symArgError, "apply: too few arguments")
	}
	f := args[0]
	last := args[len(args)-1]
	all := make([]Value, 0, 16)
	all = append(all, args[1:len(args)-1]...)
	for IsCons(last) {
		all = append(all, carFast(last))
		last = cdrFast(last)
	}
	return g.applyValue(f, all)
}

// --- tables ---

func bTable(g *Interp, args []Value) Value {
	t := NewTable()
	for i := 0; i+1 < len(args); i += 2 {
		t.Put(args[i], args[i+1])
	}
	return g.box(boxTable{t})
}
func bGet(g *Interp, args []Value) Value {
	t := AsTable(args[0])
	if t == nil {
		raise(symTypeError, "get: expected table")
	}
	v, ok := t.Get(args[1])
	if !ok {
		if len(args) > 2 {
			return args[2]
		}
		raise(symError, "key not found")
	}
	return v
}
func bPut(g *Interp, args []Value) Value {
	t := AsTable(args[0])
	if t == nil {
		raise(symTypeError, "put!: expected table")
	}
	t.Put(args[1], args[2])
	return args[2]
}
func bDel(g *Interp, args []Value) Value {
	t := AsTable(args[0])
	if t == nil {
		raise(symTypeError, "del!: expected table")
	}
	t.Del(args[1])
	return Nil
}
func bHas(g *Interp, args []Value) Value {
	t := AsTable(args[0])
	if t == nil {
		raise(symTypeError, "has?: expected table")
	}
	_, ok := t.Get(args[1])
	return BoolValue(ok)
}

// --- strings ---

func bString(g *Interp, args []Value) Value {
	var sb strings.Builder
	for _, a := range args {
		printValue(&sb, a, false)
	}
	return g.NewString(sb.String())
}
func bStringLength(g *Interp, args []Value) Value {
	return FixnumValue(int64(len(GoString(args[0]))))
}
func bStringSub(g *Interp, args []Value) Value {
	s := GoString(args[0])
	a := toInt(args[1], "string.sub")
	b := toInt(args[2], "string.sub")
	if a < 0 || b < a || int(b) > len(s) {
		raise(symBoundsError, "string.sub: index out of bounds")
	}
	return g.NewString(s[a:b])
}
func bStringFind(g *Interp, args []Value) Value {
	hay := GoString(args[0])
	needle := GoString(args[1])
	off := 0
	if len(args) > 2 {
		off = int(toInt(args[2], "string.find"))
	}
	if off > len(hay) {
		return False
	}
	i := strings.Index(hay[off:], needle)
	if i < 0 {
		return False
	}
	return FixnumValue(int64(off + i))
}
func bStringChar(g *Interp, args []Value) Value {
	s := GoString(args[0])
	i := toInt(args[1], "string.char")
	return CharValue(rune(s[i]))
}
func bStringInc(g *Interp, args []Value) Value {
	// (string.inc s i n): next char index
	s := GoString(args[0])
	i := toInt(args[1], "string.inc")
	n := toInt(args[2], "string.inc")
	for ; n > 0; n-- {
		i++
		for int(i) < len(s) && (s[i]&0xc0) == 0x80 {
			i++
		}
	}
	return FixnumValue(i)
}
func bStringDec(g *Interp, args []Value) Value {
	s := GoString(args[0])
	i := toInt(args[1], "string.dec")
	n := toInt(args[2], "string.dec")
	for ; n > 0; n-- {
		i--
		for i > 0 && (s[i]&0xc0) == 0x80 {
			i--
		}
	}
	return FixnumValue(i)
}
func bSymbol(g *Interp, args []Value) Value {
	return Symbol(GoString(args[0]))
}
func bGensym(g *Interp, args []Value) Value {
	g.heap.gensymN++
	return SymbolValue(internString(fmt.Sprintf("#:g%d", g.heap.gensymN)))
}

// --- io ---

func bPrinc(g *Interp, args []Value) Value {
	var sb strings.Builder
	for _, a := range args {
		printValue(&sb, a, false)
	}
	fmt.Fprint(g.stdout, sb.String())
	return Nil
}
func bPrint(g *Interp, args []Value) Value {
	var sb strings.Builder
	for _, a := range args {
		printValue(&sb, a, true)
	}
	fmt.Fprint(g.stdout, sb.String())
	return Nil
}
func bNewline(g *Interp, args []Value) Value {
	fmt.Fprintln(g.stdout)
	return Nil
}

// --- misc ---

func bError(g *Interp, args []Value) Value {
	var sb strings.Builder
	for _, a := range args {
		printValue(&sb, a, false)
	}
	raise(symError, "%s", sb.String())
	return Nil
}
func bRaise(g *Interp, args []Value) Value {
	raise(symError, "%s", DisplayString(args[0]))
	return Nil
}
func bTime(g *Interp, args []Value) Value {
	// (time expr-thunk) — but femtolisp's time is a macro. As builtin,
	// we just return current time. The tests use (time (fib 34)) via a
	// macro? No: in flisp, time is a macro from system.lsp. We provide
	// a simple version: (time f) calls f and prints elapsed.
	// But perf.lsp does (time (fib 34)) directly — so time must be a
	// macro. We handle that in system.lsp.
	// This builtin: return time in seconds as double.
	return DoubleValue(float64(time.Now().UnixNano()) / 1e9)
}
func bTimeNow(g *Interp, args []Value) Value {
	return DoubleValue(float64(time.Now().UnixNano()) / 1e9)
}
func bRand(g *Interp, args []Value) Value {
	g.heap.gensymN++
	n := uint64(g.heap.gensymN)*6364136223846793005 + 1442695040888963407
	return FixnumValue(int64(n>>33) & 0x3fffffff)
}
func bPar(g *Interp, args []Value) Value {
	if len(args) != 1 || AsFunction(args[0]) == nil {
		raise(symArgError, "par: expected a zero-argument thunk")
	}
	return g.newAsyncTask(args[0])
}

func bWait(g *Interp, args []Value) Value {
	if len(args) != 1 {
		raise(symArgError, "wait: expected one task")
	}
	t := AsAsyncTask(args[0])
	if t == nil {
		raise(symTypeError, "wait: expected task")
	}
	return g.waitAsyncTask(t)
}

func bEval(g *Interp, args []Value) Value {
	v, err := g.EvalTop(args[0])
	if err != nil {
		raise(symError, "%s", err.Error())
	}
	return v
}
func bLoad(g *Interp, args []Value) Value {
	if err := g.LoadFile(GoString(args[0])); err != nil {
		raise(symIOError, "%s", err.Error())
	}
	return True
}
func bExit(g *Interp, args []Value) Value {
	code := 0
	if len(args) > 0 {
		code = int(toInt(args[0], "exit"))
	}
	panic(&exitPanic{code})
}
func bVoid(g *Interp, args []Value) Value { return unspecified }
func bTopLevelValue(g *Interp, args []Value) Value {
	id := SymID(args[0])
	v, ok := g.dynLookup(id)
	if !ok {
		raise(symUnboundError, "variable %s has no value", SymName(args[0]))
	}
	return v
}
func bSetTopLevelValue(g *Interp, args []Value) Value {
	g.dynSet(SymID(args[0]), args[1])
	return args[1]
}
func bConstantp(g *Interp, args []Value) Value {
	if IsSymbol(args[0]) {
		return BoolValue(g.symIsConstant(SymID(args[0])))
	}
	return False
}
func bLogand(g *Interp, args []Value) Value {
	return FixnumValue(toInt(args[0], "logand") & toInt(args[1], "logand"))
}
func bAsh(g *Interp, args []Value) Value {
	n := toInt(args[0], "ash")
	s := toInt(args[1], "ash")
	if s >= 0 {
		return FixnumValue(n << uint(s))
	}
	return FixnumValue(n >> uint(-s))
}
func bNumberToString(g *Interp, args []Value) Value {
	return g.NewString(numberString(args[0]))
}
func bEofObject(g *Interp, args []Value) Value  { return EOFVal }
func bEofObjectP(g *Interp, args []Value) Value { return BoolValue(args[0] == EOFVal) }

type exitPanic struct{ code int }

func (e *exitPanic) Error() string { return fmt.Sprintf("exit %d", e.code) }

// printHeap is defined in print.go? No — define here to avoid cycles.
// Actually it's in heap-printer below.
