package interp

import (
	"hash/maphash"
	"sync"
)

// Symbol table: globally interned names and IDs. Runtime bindings live in each
// Interp so separate VMs do not share mutable Lisp state.

type symbol struct {
	name string
}

var (
	symMu       sync.RWMutex
	symNames    []symbol
	symByName   = map[string]uint32{}
	wellKnownMu sync.Once
)

// Pre-interned symbols (order matters only for well-known ids).
var (
	symLambda      uint32
	symQuote       uint32
	symIf          uint32
	symBegin       uint32
	symSet         uint32
	symDefine      uint32
	symDefineMac   uint32
	symLet         uint32
	symLetStar     uint32
	symLetRec      uint32
	symCond        uint32
	symAnd         uint32
	symOr          uint32
	symNot         uint32
	symWhile       uint32
	symDoTimes     uint32
	symQuasiQuote  uint32
	symUnquote     uint32
	symUnquoteS    uint32
	symLabel       uint32
	symProg1       uint32
	symTryCatch    uint32
	symMacroExpand uint32

	symTypeError    uint32
	symArgError     uint32
	symUnboundError uint32
	symBoundsError  uint32
	symDivideError  uint32
	symIOError      uint32
	symParseError   uint32
	symError        uint32

	symT uint32
	symF uint32
)

func internWellKnown() {
	wellKnownMu.Do(func() {
		for _, p := range []struct {
			name string
			dst  *uint32
		}{
			{"lambda", &symLambda}, {"quote", &symQuote}, {"if", &symIf},
			{"begin", &symBegin}, {"set!", &symSet}, {"define", &symDefine},
			{"define-macro", &symDefineMac}, {"let", &symLet}, {"let*", &symLetStar},
			{"letrec", &symLetRec}, {"cond", &symCond}, {"and", &symAnd},
			{"or", &symOr}, {"not", &symNot}, {"while", &symWhile},
			{"dotimes", &symDoTimes}, {"quasiquote", &symQuasiQuote},
			{"unquote", &symUnquote}, {"unquote-splicing", &symUnquoteS},
			{"label", &symLabel}, {"prog1", &symProg1}, {"trycatch", &symTryCatch},
			{"*macroexpand*", &symMacroExpand},
			{"type-error", &symTypeError}, {"arg-error", &symArgError},
			{"unbound-error", &symUnboundError}, {"bounds-error", &symBoundsError},
			{"divide-error", &symDivideError}, {"io-error", &symIOError},
			{"parse-error", &symParseError}, {"error", &symError},
			{"#t", &symT}, {"#f", &symF},
		} {
			*p.dst = internString(p.name)
		}
	})
}

func internString(name string) uint32 {
	symMu.RLock()
	id, ok := symByName[name]
	symMu.RUnlock()
	if ok {
		return id
	}
	symMu.Lock()
	defer symMu.Unlock()
	if id, ok := symByName[name]; ok {
		return id
	}
	id = uint32(len(symNames))
	symNames = append(symNames, symbol{name: name})
	symByName[name] = id
	return id
}

func Symbol(name string) Value { return SymbolValue(internString(name)) }

func SymName(v Value) string {
	id := SymID(v)
	symMu.RLock()
	defer symMu.RUnlock()
	if int(id) < len(symNames) {
		return symNames[id].name
	}
	return "#<bad-sym>"
}

func symNameByID(id uint32) string {
	symMu.RLock()
	defer symMu.RUnlock()
	return symNames[id].name
}

func (g *Interp) globalBinding(id uint32) *Value {
	g.ensureSymbolState(id)
	return &g.globals[id]
}

func (g *Interp) symbolValue(id uint32) (Value, bool) {
	g.ensureSymbolState(id)
	v := g.globals[id]
	if v == 0 {
		return 0, false
	}
	return v, true
}

func (g *Interp) setSymbol(id uint32, v Value) {
	g.setGlobal(id, v)
}

// Lookup returns the global binding for an interned symbol id. It reports
// ok=false when the symbol is unbound instead of raising a Lisp error.
func (g *Interp) Lookup(id uint32) (Value, bool) { return g.symbolValue(id) }

// SetGlobal binds an interned symbol id directly. v == 0 (the zero Value)
// unbinds the symbol.
func (g *Interp) SetGlobal(id uint32, v Value) { g.setGlobal(id, v) }

func (g *Interp) symIsConstant(id uint32) bool {
	g.ensureSymbolState(id)
	return g.constants[id]
}

// ---------------------------------------------------------------------------
// Tables: hash tables with value keys. Used by (table), put!/get, and
// internally for globals.

type tableEntry struct {
	key Value
	val Value
}

type Table struct {
	m     map[uint64]Value      // keys that are not heap strings
	hm    map[string]tableEntry // string keys (equal? semantics)
	other []tableEntry          // rare: heap cons/vector keys
	count int
	seed  maphash.Seed
}

func NewTable() *Table {
	return &Table{
		m:    make(map[uint64]Value),
		hm:   make(map[string]tableEntry),
		seed: maphash.MakeSeed(),
	}
}

func (t *Table) Get(k Value) (Value, bool) {
	if !IsHeap(k) {
		v, ok := t.m[uint64(k)]
		return v, ok
	}
	if IsString(k) {
		e, ok := t.hm[GoString(k)]
		return e.val, ok
	}
	for i := range t.other {
		if t.other[i].key == k || Equal(t.other[i].key, k) {
			return t.other[i].val, true
		}
	}
	return 0, false
}

func (t *Table) Put(k, v Value) {
	if !IsHeap(k) {
		if _, ok := t.m[uint64(k)]; !ok {
			t.count++
		}
		t.m[uint64(k)] = v
		return
	}
	if IsString(k) {
		s := GoString(k)
		if _, ok := t.hm[s]; !ok {
			t.count++
		}
		t.hm[s] = tableEntry{k, v}
		return
	}
	for i := range t.other {
		if t.other[i].key == k || Equal(t.other[i].key, k) {
			t.other[i].val = v
			return
		}
	}
	t.other = append(t.other, tableEntry{k, v})
	t.count++
}

func (t *Table) Del(k Value) {
	if !IsHeap(k) {
		if _, ok := t.m[uint64(k)]; ok {
			t.count--
			delete(t.m, uint64(k))
		}
		return
	}
	if IsString(k) {
		s := GoString(k)
		if _, ok := t.hm[s]; ok {
			t.count--
			delete(t.hm, s)
		}
		return
	}
	for i := range t.other {
		if t.other[i].key == k || Equal(t.other[i].key, k) {
			t.other = append(t.other[:i], t.other[i+1:]...)
			t.count--
			return
		}
	}
}

func (t *Table) Count() int { return t.count }

func (t *Table) mark(g *Interp) {
	for _, v := range t.m {
		g.mark(v)
	}
	for _, e := range t.hm {
		g.mark(e.key)
		g.mark(e.val)
	}
	for i := range t.other {
		g.mark(t.other[i].key)
		g.mark(t.other[i].val)
	}
}
