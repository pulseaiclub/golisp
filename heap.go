package golisp

import (
	"fmt"
	"reflect"
	"sync"
	"unsafe"
)

// ---------------------------------------------------------------------------
// Managed heap.
//
// All lisp objects are allocated in fixed-size chunks (so pointers never
// move). An arenaPtr encodes (chunkIdx<<32 | offset).
//
// Object layout at a chunk offset:
//
//	cons:   header | car | cdr           (3 cells)
//	vector: header | elems...            (1+n cells)
//	string: header | blobOff | blobLen   (3 cells)
//	boxed:  header | *Boxed              (2 cells)
//
// header: low 3 bits = objType, rest = total cells, bit62 = mark.
// ---------------------------------------------------------------------------

type objType uint8

const (
	tFree objType = iota
	tCons
	tVector
	tString
	tBoxed
)

func (t objType) String() string {
	switch t {
	case tCons:
		return "cons"
	case tVector:
		return "vector"
	case tString:
		return "string"
	case tBoxed:
		return "boxed"
	}
	return "free"
}

const (
	hdrTagMask = 0x7
	hdrBits    = 3
	markBit    = 1 << 62
	sizeMask   = ^uint64(markBit) &^ hdrTagMask
)

func headerSize(hdr Value) int { return int(uint64(hdr) & sizeMask >> hdrBits) }

func makeHeader(t objType, size int) Value {
	return Value(uint64(size)<<hdrBits | uint64(t))
}

type Boxed struct {
	marked bool
	data   any
}

type (
	boxFunction struct{ fn *Function }
	boxBuiltin  struct{ b *Builtin }
	boxTable    struct{ t *Table }
	boxStream   struct{ s *IOStream }
	boxGensym   struct{ id uint32 }
	boxCell     struct{ c *FrameCell }
	boxListener struct{ l *Listener }
	boxGoFunc   struct {
		v    reflect.Value
		name string
	}
	boxGoValue struct {
		v    reflect.Value
		name string
	}
)

type arenaPtr int64

func (a arenaPtr) chunk() int { return int(int64(a) >> 32) }
func (a arenaPtr) off() int   { return int(int64(a) & 0xffffffff) }

type bigBlock struct {
	idx  arenaPtr
	size int
}

type heap struct {
	chunks   [][]Value
	chunkTop int // bump offset in last chunk

	freeByN [9][]arenaPtr
	bigFree []bigBlock
	bytes   int

	boxes   []*Boxed
	gensymN uint32
	gcOff   bool

	threshold int
}

const arenaChunk = 1 << 16

func newHeap() *heap {
	h := &heap{threshold: arenaChunk}
	h.growArena(arenaChunk)
	return h
}

func (h *heap) growArena(need int) {
	n := arenaChunk
	for n < need {
		n *= 2
	}
	h.chunks = append(h.chunks, make([]Value, n))
	h.chunkTop = 0
}

func (h *heap) allocCells(n int) arenaPtr {
	if n <= 8 {
		if fl := h.freeByN[n]; len(fl) > 0 {
			idx := fl[len(fl)-1]
			h.freeByN[n] = fl[:len(fl)-1]
			h.bytes += n
			return idx
		}
	} else {
		for i, b := range h.bigFree {
			if b.size >= n {
				idx := b.idx
				if b.size > n {
					h.bigFree[i] = bigBlock{b.idx + arenaPtr(n), b.size - n}
				} else {
					h.bigFree = append(h.bigFree[:i], h.bigFree[i+1:]...)
				}
				h.bytes += n
				return idx
			}
		}
	}
	ci := len(h.chunks) - 1
	if h.chunkTop+n > len(h.chunks[ci]) {
		h.growArena(n)
		ci = len(h.chunks) - 1
	}
	off := h.chunkTop
	h.chunkTop += n
	h.bytes += n
	return arenaPtr(int64(ci)<<32 | int64(off))
}

func (h *heap) freeCells(idx arenaPtr, n int) {
	if n <= 8 {
		h.freeByN[n] = append(h.freeByN[n], idx)
	} else {
		h.bigFree = append(h.bigFree, bigBlock{idx, n})
	}
}

// cellValue returns a Value pointing to arena cell at idx.
func (h *heap) cellValue(idx arenaPtr) Value {
	return boxPtr(unsafe.Pointer(&h.chunks[idx.chunk()][idx.off()]))
}

func heapObjPtr(v Value) *Value {
	return (*Value)(unsafe.Pointer(uintptr(uint64(v) &^ qNaN)))
}

func heapType(v Value) objType {
	return objType(uint64(*heapObjPtr(v)) & hdrTagMask)
}

func IsCons(v Value) bool   { return IsHeap(v) && heapType(v) == tCons }
func IsVector(v Value) bool { return IsHeap(v) && heapType(v) == tVector }
func IsString(v Value) bool { return IsHeap(v) && heapType(v) == tString }
func IsBoxed(v Value) bool  { return IsHeap(v) && heapType(v) == tBoxed }

func heapHeader(v Value) Value { return *heapObjPtr(v) }

// --- cons ---

func (g *Interp) Cons(a, d Value) Value {
	idx := g.heap.allocCells(3)
	ch := g.heap.chunks[idx.chunk()]
	ch[idx.off()] = makeHeader(tCons, 3)
	ch[idx.off()+1] = a
	ch[idx.off()+2] = d
	return g.heap.cellValue(idx)
}

func Car(v Value) Value {
	if !IsCons(v) {
		raise(symTypeError, "car: expected cons, got %s", typeName(v))
	}
	return *(*Value)(unsafe.Pointer(uintptr(uint64(v)&^qNaN) + 8))
}

func Cdr(v Value) Value {
	if !IsCons(v) {
		raise(symTypeError, "cdr: expected cons, got %s", typeName(v))
	}
	return *(*Value)(unsafe.Pointer(uintptr(uint64(v)&^qNaN) + 16))
}

func carFast(v Value) Value {
	return *(*Value)(unsafe.Pointer(uintptr(uint64(v)&^qNaN) + 8))
}
func cdrFast(v Value) Value {
	return *(*Value)(unsafe.Pointer(uintptr(uint64(v)&^qNaN) + 16))
}

func SetCar(v, x Value) {
	if !IsCons(v) {
		raise(symTypeError, "set-car!: expected cons")
	}
	*(*Value)(unsafe.Pointer(uintptr(uint64(v)&^qNaN) + 8)) = x
}

func SetCdr(v, x Value) {
	if !IsCons(v) {
		raise(symTypeError, "set-cdr!: expected cons")
	}
	*(*Value)(unsafe.Pointer(uintptr(uint64(v)&^qNaN) + 16)) = x
}

// --- vectors ---

func (g *Interp) NewVector(n int, init Value) Value {
	cells := 1 + n
	if n == 0 {
		cells = 2
	}
	idx := g.heap.allocCells(cells)
	ch := g.heap.chunks[idx.chunk()]
	ch[idx.off()] = makeHeader(tVector, cells)
	for i := 0; i < n; i++ {
		ch[idx.off()+1+i] = init
	}
	return g.heap.cellValue(idx)
}

func vectorData(v Value) []Value {
	p := heapObjPtr(v)
	n := headerSize(*p) - 1
	base := uintptr(unsafe.Pointer(p)) + 8
	return unsafe.Slice((*Value)(unsafe.Pointer(base)), n)
}

func VectorLen(v Value) int {
	if !IsVector(v) {
		raise(symTypeError, "length: expected vector")
	}
	return headerSize(heapHeader(v)) - 1
}

func VectorRef(v Value, i int) Value {
	d := vectorData(v)
	if i < 0 || i >= len(d) {
		raise(symBoundsError, "aref: index %d out of bounds", i)
	}
	return d[i]
}

func VectorSet(v Value, i int, x Value) {
	d := vectorData(v)
	if i < 0 || i >= len(d) {
		raise(symBoundsError, "aset!: index %d out of bounds", i)
	}
	d[i] = x
}

// --- strings ---

// The string arena is shared by every interpreter. Strings are immutable and
// the arena is append-only, so a single process-wide blob is safe and lets
// string access work without a back-pointer to the owning interpreter.
var (
	stringArenaMu sync.Mutex
	stringArena   []byte
)

func (g *Interp) NewString(s string) Value {
	stringArenaMu.Lock()
	off := len(stringArena)
	stringArena = append(stringArena, s...)
	stringArenaMu.Unlock()
	idx := g.heap.allocCells(3)
	ch := g.heap.chunks[idx.chunk()]
	ch[idx.off()] = makeHeader(tString, 3)
	ch[idx.off()+1] = Value(uint64(off))
	ch[idx.off()+2] = Value(uint64(len(s)))
	return g.heap.cellValue(idx)
}

func StringBytes(v Value) []byte {
	s := GoString(v)
	return []byte(s)
}

func GoString(v Value) string {
	if !IsString(v) {
		raise(symTypeError, "string: expected string, got %s", typeName(v))
	}
	p := heapObjPtr(v)
	off := uint64(*(*Value)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + 8)))
	n := uint64(*(*Value)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + 16)))
	stringArenaMu.Lock()
	out := string(stringArena[off : off+n])
	stringArenaMu.Unlock()
	return out
}

// --- boxed ---

func (g *Interp) box(data any) Value {
	b := &Boxed{data: data}
	g.heap.boxes = append(g.heap.boxes, b)
	idx := g.heap.allocCells(2)
	ch := g.heap.chunks[idx.chunk()]
	ch[idx.off()] = makeHeader(tBoxed, 2)
	ch[idx.off()+1] = Value(uint64(uintptr(unsafe.Pointer(b))))
	return g.heap.cellValue(idx)
}

func boxOf(v Value) *Boxed {
	return (*Boxed)(unsafe.Pointer(uintptr(*(*Value)(unsafe.Pointer(uintptr(uint64(v)&^qNaN) + 8)))))
}

func AsFunction(v Value) *Function {
	if IsBoxed(v) {
		if bf, ok := boxOf(v).data.(boxFunction); ok {
			return bf.fn
		}
	}
	return nil
}

func AsBuiltin(v Value) *Builtin {
	if IsBoxed(v) {
		if bb, ok := boxOf(v).data.(boxBuiltin); ok {
			return bb.b
		}
	}
	return nil
}

func AsTable(v Value) *Table {
	if IsBoxed(v) {
		if bt, ok := boxOf(v).data.(boxTable); ok {
			return bt.t
		}
	}
	return nil
}

func AsListener(v Value) *Listener {
	if IsBoxed(v) {
		if bl, ok := boxOf(v).data.(boxListener); ok {
			return bl.l
		}
	}
	return nil
}

func AsGoFunc(v Value) (reflect.Value, string, bool) {
	if IsBoxed(v) {
		if x, ok := boxOf(v).data.(boxGoFunc); ok {
			return x.v, x.name, true
		}
	}
	return reflect.Value{}, "", false
}

func AsGoValue(v Value) (reflect.Value, string, bool) {
	if IsBoxed(v) {
		if x, ok := boxOf(v).data.(boxGoValue); ok {
			return x.v, x.name, true
		}
	}
	return reflect.Value{}, "", false
}

func AsStream(v Value) *IOStream {
	if IsBoxed(v) {
		if bs, ok := boxOf(v).data.(boxStream); ok {
			return bs.s
		}
	}
	return nil
}

// --- GC ---

func (g *Interp) maybeGC() {
	if !g.heap.gcOff && g.heap.bytes >= g.heap.threshold {
		g.gc()
	}
}

func (g *Interp) gc() {
	h := g.heap
	// mark roots
	symMu.RLock()
	for i := range symNames {
		if symNames[i].binding != 0 {
			g.mark(symNames[i].binding)
		}
		if symNames[i].function != 0 {
			g.mark(symNames[i].function)
		}
	}
	symMu.RUnlock()
	for _, v := range g.stack[:g.sp] {
		g.mark(v)
	}
	for _, v := range g.globals {
		g.mark(v)
	}
	for _, c := range g.cellPool {
		g.mark(c.v)
	}
	for _, t := range g.tryStack {
		g.mark(t.handler)
	}

	// sweep each chunk
	h.bytes = 0
	for ci := range h.chunks {
		ch := h.chunks[ci]
		top := len(ch)
		if ci == len(h.chunks)-1 {
			top = h.chunkTop
		}
		for i := 0; i < top; {
			hdr := ch[i]
			if hdr == 0 {
				i++
				continue
			}
			size := headerSize(hdr)
			if size <= 0 {
				i++
				continue
			}
			if uint64(hdr)&markBit != 0 {
				ch[i] = Value(uint64(hdr) &^ markBit)
				h.bytes += size
			} else {
				h.freeCells(arenaPtr(int64(ci)<<32|int64(i)), size)
				ch[i] = 0
			}
			i += size
		}
	}
	// sweep boxes
	boxes := h.boxes[:0]
	for _, b := range h.boxes {
		if b.marked {
			b.marked = false
			boxes = append(boxes, b)
		}
	}
	h.boxes = boxes

	h.threshold = h.bytes * 2
	if h.threshold < arenaChunk {
		h.threshold = arenaChunk
	}
}

func (g *Interp) mark(v Value) {
	for {
		if !IsHeap(v) {
			return
		}
		p := heapObjPtr(v)
		hdr := *p
		if uint64(hdr)&markBit != 0 {
			return
		}
		*p = Value(uint64(hdr) | markBit)
		switch objType(uint64(hdr) & hdrTagMask) {
		case tCons:
			g.mark(*(*Value)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + 8)))
			v = *(*Value)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + 16))
		case tVector:
			n := headerSize(hdr) - 1
			base := uintptr(unsafe.Pointer(p)) + 8
			data := unsafe.Slice((*Value)(unsafe.Pointer(base)), n)
			for _, e := range data {
				g.mark(e)
			}
			return
		case tString:
			return
		case tBoxed:
			b := (*Boxed)(unsafe.Pointer(uintptr(*(*Value)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + 8)))))
			b.marked = true
			switch d := b.data.(type) {
			case boxFunction:
				fn := d.fn
				for _, c := range fn.consts {
					g.mark(c)
				}
				for _, up := range fn.upvals {
					if up != nil {
						g.mark(up.v)
					}
				}
			case boxBuiltin:
			case boxTable:
				d.t.mark(g)
			case boxCell:
				if d.c != nil {
					g.mark(d.c.v)
				}
			case boxStream, boxListener, boxGensym, boxGoFunc, boxGoValue:
				// These hold no Lisp values, so the Go runtime keeps
				// whatever they reference alive.
			}
			return
		default:
			return
		}
	}
}

var _ = fmt.Sprintf
