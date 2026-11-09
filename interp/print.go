package interp

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

func typeName(v Value) string {
	switch {
	case v == Nil:
		return "()"
	case v == True || v == False:
		return "boolean"
	case v == EOFVal:
		return "eof"
	case IsFixnum(v):
		return "fixnum"
	case IsDouble(v):
		return "number"
	case IsChar(v):
		return "char"
	case IsSymbol(v):
		return "symbol"
	case IsHeap(v):
		return heapType(v).String()
	}
	return "unknown"
}

// numberString formats like C's %g-ish scheme printing: doubles keep a
// decimal point or exponent so they print differently from integers.
func numberString(v Value) string {
	if IsFixnum(v) {
		return strconv.FormatInt(Fixnum(v), 10)
	}
	f := math.Float64frombits(uint64(v))
	switch {
	case math.IsInf(f, 1):
		return "+inf.0"
	case math.IsInf(f, -1):
		return "-inf.0"
	case math.IsNaN(f):
		return "+nan.0"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// printValue renders v for princ (display) or print (write).
func printValue(sb *strings.Builder, v Value, write bool) {
	switch {
	case v == Nil:
		sb.WriteString("()")
	case v == True:
		sb.WriteString("#t")
	case v == False:
		sb.WriteString("#f")
	case v == EOFVal:
		sb.WriteString("#<eof>")
	case IsFixnum(v) || IsDouble(v):
		sb.WriteString(numberString(v))
	case IsChar(v):
		if write {
			printChar(sb, Char(v))
		} else {
			sb.WriteRune(Char(v))
		}
	case IsSymbol(v):
		sb.WriteString(SymName(v))
	case IsHeap(v):
		printHeap(sb, v, write)
	default:
		fmt.Fprintf(sb, "#<bad-value %016x>", uint64(v))
	}
}

func printHeap(sb *strings.Builder, v Value, write bool) {
	switch heapType(v) {
	case tCons:
		sb.WriteByte('(')
		printValue(sb, carFast(v), write)
		rest := cdrFast(v)
		for IsCons(rest) {
			sb.WriteByte(' ')
			printValue(sb, carFast(rest), write)
			rest = cdrFast(rest)
		}
		if rest != Nil {
			sb.WriteString(" . ")
			printValue(sb, rest, write)
		}
		sb.WriteByte(')')
	case tVector:
		sb.WriteByte('[')
		d := vectorData(v)
		for i, e := range d {
			if i > 0 {
				sb.WriteByte(' ')
			}
			printValue(sb, e, write)
		}
		sb.WriteByte(']')
	case tString:
		printString(sb, GoString(v), write)
	case tBoxed:
		b := boxOf(v)
		switch d := b.data.(type) {
		case boxFunction:
			fmt.Fprintf(sb, "#<function %s>", d.fn.name)
		case boxBuiltin:
			fmt.Fprintf(sb, "#<builtin %s>", d.b.name)
		case boxTable:
			fmt.Fprintf(sb, "#<table %d entries>", d.t.Count())
		case boxStream:
			if d.s.closed {
				fmt.Fprintf(sb, "#<stream %s closed>", d.s.name)
			} else {
				fmt.Fprintf(sb, "#<stream %s>", d.s.name)
			}
		case boxListener:
			if d.l.closed {
				fmt.Fprintf(sb, "#<listener %s closed>", d.l.name)
			} else {
				fmt.Fprintf(sb, "#<listener %s>", d.l.name)
			}
		case boxGensym:
			fmt.Fprintf(sb, "#:g%d", d.id)
		case boxCell:
			sb.WriteString("#<cell>")
		case boxGoFunc:
			fmt.Fprintf(sb, "#<go-func %s>", d.name)
		case boxGoValue:
			if d.v.IsValid() {
				if d.v.Kind() == reflect.Ptr && d.v.IsNil() {
					sb.WriteString("#<go nil>")
				} else {
					fmt.Fprintf(sb, "#<go %s>", d.v.Type())
				}
			} else {
				sb.WriteString("#<go nil>")
			}
		default:
			sb.WriteString("#<boxed>")
		}
	default:
		sb.WriteString("#<heap-object>")
	}
}

func printChar(sb *strings.Builder, r rune) {
	sb.WriteString("#\\")
	switch r {
	case ' ':
		sb.WriteString("space")
	case '\n':
		sb.WriteString("newline")
	case '\t':
		sb.WriteString("tab")
	default:
		sb.WriteRune(r)
	}
}

func printString(sb *strings.Builder, s string, write bool) {
	if !write {
		sb.WriteString(s)
		return
	}
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString("\\\"")
		case '\\':
			sb.WriteString("\\\\")
		case '\n':
			sb.WriteString("\\n")
		case '\t':
			sb.WriteString("\\t")
		case '\r':
			sb.WriteString("\\r")
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
}

func DisplayString(v Value) string {
	var sb strings.Builder
	printValue(&sb, v, false)
	return sb.String()
}

func WriteStringOf(v Value) string {
	var sb strings.Builder
	printValue(&sb, v, true)
	return sb.String()
}

func (v Value) String() string { return DisplayString(v) }

var _ = utf8.RuneLen
