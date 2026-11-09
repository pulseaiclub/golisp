package golisp

import (
	"math"
	"unsafe"
)

// Value is a NaN-boxed 64-bit lisp value.
//
// Encoding (little-endian uint64):
//
//	double:    any IEEE-754 double that is not one of the tagged NaNs below
//	heap ptr:  0x7ff8_0000_0000_0000 | ptr  (48-bit user-space pointers)
//	fixnum:    0xfffc_xxxx_xxxx_xxxx  (48-bit signed integer)
//	char:      0xfffd_0000_00xx_xxxx  (rune)
//	symbol:    0xfffe_0000_xxxx_xxxx  (interned symbol id)
//	specials:  0x7ff8_0000_0000_0001..6  (nil, #t, #f, eof, gensym...)
type Value uint64

const (
	qNaN = 0x7ff8000000000000

	tagNil   = qNaN | 1
	tagTrue  = qNaN | 2
	tagFalse = qNaN | 3
	tagEOF   = qNaN | 4

	tagFixnum = 0xfffc000000000000
	tagChar   = 0xfffd000000000000
	tagSym    = 0xfffe000000000000
	tagMask   = 0xffff000000000000
)

const (
	Nil    Value = tagNil
	True   Value = tagTrue
	False  Value = tagFalse
	EOFVal Value = tagEOF
)

func BoolValue(b bool) Value {
	if b {
		return True
	}
	return False
}

func FixnumValue(n int64) Value {
	return Value(uint64(n)&0x0000ffffffffffff | tagFixnum)
}

func CharValue(r rune) Value { return Value(uint64(uint32(r)) | tagChar) }

func SymbolValue(id uint32) Value { return Value(uint64(id) | tagSym) }

func IsNil(v Value) bool    { return v == Nil }
func IsTrue(v Value) bool   { return v == True }
func IsFalse(v Value) bool  { return v == False }
func IsBool(v Value) bool   { return v == True || v == False }
func IsFixnum(v Value) bool { return uint64(v)&tagMask == tagFixnum }
func IsChar(v Value) bool   { return uint64(v)&tagMask == tagChar }
func IsSymbol(v Value) bool { return uint64(v)&tagMask == tagSym }

func Fixnum(v Value) int64 { return int64(uint64(v)<<16) >> 16 }
func Char(v Value) rune    { return rune(uint32(v)) }
func SymID(v Value) uint32 { return uint32(v) }

// IsDouble: exponent bits set but tag bits not matching immediates.
func IsDouble(v Value) bool {
	u := uint64(v)
	if u&tagMask == tagFixnum || u&tagMask == tagChar || u&tagMask == tagSym {
		return false
	}
	return u&0x7ff8000000000000 != qNaN || u == qNaN-0x0008000000000000
}

func IsNumber(v Value) bool { return IsFixnum(v) || IsDouble(v) }

func DoubleValue(f float64) Value {
	return Value(math.Float64bits(f))
}

func Double(v Value) float64 {
	if IsFixnum(v) {
		return float64(Fixnum(v))
	}
	return math.Float64frombits(uint64(v))
}

// IsHeap reports whether v is a pointer into the managed heap.
// Boxed pointers have exponent bits 0x7ff and mantissa sign bit set
// (0x7ff8...); immediates like nil/#t/#f have small payload (< 16).
func IsHeap(v Value) bool {
	u := uint64(v)
	return u&0x7fff000000000000 == 0x7ff8000000000000 && u&0x0000ffffffffffff > 0xf
}

// HeapPointer decodes the raw pointer (as unsafe.Pointer).
func HeapPointer(v Value) unsafe.Pointer {
	return unsafe.Pointer(uintptr(uint64(v) &^ qNaN))
}

func boxPtr(p unsafe.Pointer) Value {
	return Value(uint64(uintptr(p)) | qNaN)
}

// IsObject is an alias for IsHeap (GC-relevant values).
func IsObject(v Value) bool { return IsHeap(v) }

// Truthy implements femtolisp truth: only #f is false.
func Truthy(v Value) bool { return v != False }

// IsCons is defined in heap.go (needs objType).
