package interp

import (
	"fmt"
	"math"
	"strings"
)

// LispError is raised via panic and caught by trycatch / top-level.
type LispError struct {
	kind uint32 // symbol id of error type
	msg  string
	args []Value
}

func (e *LispError) Error() string { return e.msg }

func raise(kind uint32, format string, args ...interface{}) {
	panic(&LispError{kind: kind, msg: fmt.Sprintf(format, args...)})
}

// Equal implements femtolisp's equal? (deep structural equality).
func Equal(a, b Value) bool {
	if a == b {
		return true
	}
	if !IsHeap(a) || !IsHeap(b) {
		// fixnum vs double numeric compare
		if IsNumber(a) && IsNumber(b) {
			return Double(a) == Double(b)
		}
		return false
	}
	ta, tb := heapType(a), heapType(b)
	if ta != tb {
		return false
	}
	switch ta {
	case tCons:
		// iterative to bound recursion depth on long lists
		for IsCons(a) && IsCons(b) {
			if !Equal(carFast(a), carFast(b)) {
				return false
			}
			a = cdrFast(a)
			b = cdrFast(b)
			if a == b {
				return true
			}
		}
		return Equal(a, b)
	case tVector:
		da, db := vectorData(a), vectorData(b)
		if len(da) != len(db) {
			return false
		}
		for i := range da {
			if !Equal(da[i], db[i]) {
				return false
			}
		}
		return true
	case tString:
		return GoString(a) == GoString(b)
	case tBoxed:
		return a == b
	}
	return false
}

func Eqv(a, b Value) bool {
	if a == b {
		return true
	}
	if IsNumber(a) && IsNumber(b) {
		return Double(a) == Double(b)
	}
	return false
}

// Compare returns -1, 0, 1 like femtolisp's compare.
func Compare(a, b Value) int {
	if a == b {
		return 0
	}
	if IsNumber(a) && IsNumber(b) {
		fa, fb := Double(a), Double(b)
		if fa < fb {
			return -1
		}
		if fa > fb {
			return 1
		}
		return 0
	}
	if IsSymbol(a) && IsSymbol(b) {
		return strings.Compare(SymName(a), SymName(b))
	}
	if IsString(a) && IsString(b) {
		return strings.Compare(GoString(a), GoString(b))
	}
	if IsChar(a) && IsChar(b) {
		ca, cb := Char(a), Char(b)
		if ca < cb {
			return -1
		}
		return 1
	}
	// mixed types: order by tag
	ta, tb := typeOrder(a), typeOrder(b)
	if ta < tb {
		return -1
	}
	if ta > tb {
		return 1
	}
	if IsCons(a) && IsCons(b) {
		c := Compare(carFast(a), carFast(b))
		if c != 0 {
			return c
		}
		return Compare(cdrFast(a), cdrFast(b))
	}
	if uint64(a) < uint64(b) {
		return -1
	}
	return 1
}

func typeOrder(v Value) int {
	switch {
	case v == Nil:
		return 0
	case IsFixnum(v) || IsDouble(v):
		return 1
	case IsSymbol(v):
		return 2
	case IsString(v):
		return 3
	case IsChar(v):
		return 4
	case IsCons(v):
		return 5
	case IsVector(v):
		return 6
	}
	return 7
}

// Numeric arithmetic --------------------------------------------------------

func toFloat(v Value) float64 {
	if IsFixnum(v) {
		return float64(Fixnum(v))
	}
	if IsDouble(v) {
		return math.Float64frombits(uint64(v))
	}
	raise(symTypeError, "number expected, got %s", typeName(v))
	return 0
}

func toInt(v Value, what string) int64 {
	if IsFixnum(v) {
		return Fixnum(v)
	}
	if IsDouble(v) {
		f := math.Float64frombits(uint64(v))
		return int64(f)
	}
	raise(symTypeError, "%s: integer expected, got %s", what, typeName(v))
	return 0
}

const fixnumMax = int64(1)<<47 - 1
const fixnumMin = -int64(1) << 47

func numAdd(a, b Value) Value {
	if IsFixnum(a) && IsFixnum(b) {
		r := Fixnum(a) + Fixnum(b)
		if r >= fixnumMin && r <= fixnumMax {
			return FixnumValue(r)
		}
		return DoubleValue(float64(Fixnum(a)) + float64(Fixnum(b)))
	}
	return DoubleValue(toFloat(a) + toFloat(b))
}

func numSub(a, b Value) Value {
	if IsFixnum(a) && IsFixnum(b) {
		r := Fixnum(a) - Fixnum(b)
		if r >= fixnumMin && r <= fixnumMax {
			return FixnumValue(r)
		}
		return DoubleValue(float64(Fixnum(a)) - float64(Fixnum(b)))
	}
	return DoubleValue(toFloat(a) - toFloat(b))
}

func numMul(a, b Value) Value {
	if IsFixnum(a) && IsFixnum(b) {
		x, y := Fixnum(a), Fixnum(b)
		if x == 0 || y == 0 {
			return FixnumValue(0)
		}
		r := x * y
		if r/y == x && r >= fixnumMin && r <= fixnumMax {
			return FixnumValue(r)
		}
		return DoubleValue(float64(x) * float64(y))
	}
	return DoubleValue(toFloat(a) * toFloat(b))
}

// numDiv is femtolisp's /: always double result.
func numDiv(a, b Value) Value {
	fa, fb := toFloat(a), toFloat(b)
	if fb == 0 {
		raise(symDivideError, "/: division by zero")
	}
	return DoubleValue(fa / fb)
}

// numIDiv is div0: truncate toward zero, fixnum when both fixnum.
func numIDiv(a, b Value) Value {
	if IsFixnum(a) && IsFixnum(b) {
		y := Fixnum(b)
		if y == 0 {
			raise(symDivideError, "div0: division by zero")
		}
		return FixnumValue(Fixnum(a) / y)
	}
	fa, fb := toFloat(a), toFloat(b)
	if fb == 0 {
		raise(symDivideError, "div0: division by zero")
	}
	return DoubleValue(math.Trunc(fa / fb))
}

func numLt(a, b Value) bool {
	if IsFixnum(a) && IsFixnum(b) {
		return Fixnum(a) < Fixnum(b)
	}
	return toFloat(a) < toFloat(b)
}

func numEq(a, b Value) bool {
	if IsFixnum(a) && IsFixnum(b) {
		return a == b
	}
	if IsNumber(a) && IsNumber(b) {
		return toFloat(a) == toFloat(b)
	}
	if IsSymbol(a) && IsSymbol(b) {
		return a == b
	}
	raise(symTypeError, "=: number expected")
	return false
}
