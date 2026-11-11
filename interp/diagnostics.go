package interp

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// SourceLocation identifies the start of an enclosing source form. Generated
// macro code inherits that form's location; this is not a bytecode PC location.
type SourceLocation struct {
	File   string
	Line   int
	Column int
}

type TraceFrame struct {
	Function string
	Source   SourceLocation
}

// ErrorTrace returns a copy, safe to inspect even for errors shared by tasks.
func ErrorTrace(err error) []TraceFrame {
	var le *LispError
	if errors.As(err, &le) {
		return append([]TraceFrame(nil), le.trace...)
	}
	return nil
}

// FormatError adds source and call context without changing Error(), error
// identity, or the Lisp condition shape consumed by existing programs.
func FormatError(err error) string {
	if err == nil {
		return ""
	}
	var out strings.Builder
	out.WriteString(err.Error())
	for _, frame := range ErrorTrace(err) {
		fmt.Fprintf(&out, "\n  at %s", frame.Function)
		if frame.Source.Line > 0 {
			fmt.Fprintf(&out, " (%s:%d:%d)", frame.Source.File, frame.Source.Line, frame.Source.Column)
		}
	}
	return out.String()
}

func traceError(err *LispError, frame TraceFrame) *LispError {
	clone := *err
	clone.trace = append(append([]TraceFrame(nil), err.trace...), frame)
	return &clone
}

func sourceLocation(src, file string, pos int) SourceLocation {
	if pos > len(src) {
		pos = len(src)
	}
	prefix := src[:pos]
	line := strings.Count(prefix, "\n") + 1
	start := strings.LastIndexByte(prefix, '\n') + 1
	return SourceLocation{File: file, Line: line, Column: utf8.RuneCountInString(prefix[start:]) + 1}
}

func (g *Interp) readLocated(src, file string) ([]Value, []SourceLocation, error) {
	r := &reader{src: src, g: g}
	var forms []Value
	var locations []SourceLocation
	for {
		r.skipWS()
		if r.err != nil {
			err := r.err
			if le, ok := err.(*LispError); ok {
				err = traceError(le, TraceFrame{Function: "read", Source: sourceLocation(src, file, r.pos)})
			}
			return forms, locations, err
		}
		if r.pos >= len(src) {
			return forms, locations, nil
		}
		location := sourceLocation(src, file, r.pos)
		v, err := r.read()
		if err == nil {
			err = r.err
		}
		if err != nil {
			if le, ok := err.(*LispError); ok {
				err = traceError(le, TraceFrame{Function: "read", Source: sourceLocation(src, file, r.pos)})
			}
			return forms, locations, err
		}
		forms = append(forms, v)
		locations = append(locations, location)
	}
}
