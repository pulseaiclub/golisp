package interp

import (
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

// Count recursion markers even inside strings and comments. Counting all of
// them (rather than subtracting closing delimiters) deliberately overestimates
// nesting, including quote prefixes and recursive datum comments.
func boundedReaderFuzzInput(src string) bool {
	if len(src) > 2048 || strings.Contains(src, "#.") {
		return false
	}
	markers := 0
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '(', ')', '[', ']', '{', '}', '\'', '`', ',', '#':
			markers++
		}
	}
	return markers <= 64
}

func TestReadMalformedReturnsError(t *testing.T) {
	for _, src := range []string{
		")", "]", "(", "[", "(a", "(a .", "(a . )", "#(", "#",
		"'", "`", ",", ",@", `"unterminated`, `"trailing\`, "{", "}", ".",
	} {
		t.Run(src, func(t *testing.T) {
			g := NewInterp()
			defer runtime.KeepAlive(g)
			if _, err := g.ReadAll(src); err == nil {
				t.Fatalf("ReadAll(%q) succeeded for malformed input", src)
			}
		})
	}
}

func FuzzReadAll(f *testing.F) {
	for _, src := range []string{
		"", "42 -3 .5 0xff", "(a . b)", "[1 2 #t]", "#(1 2)",
		"'(a `(b ,c ,@d))", `"hello\n世界"`, "|a b|", `#\space`,
		"; line\n#| outer #| inner |# |# 42", "#; (ignored) kept",
		"(", "(a .", ")", "#", `"unterminated`, "{", "\x00",
	} {
		f.Add(src)
	}
	f.Fuzz(func(t *testing.T, src string) {
		if !boundedReaderFuzzInput(src) {
			t.Skip()
		}
		g := NewInterp()
		defer runtime.KeepAlive(g)
		// Parse only. A returned error is valid; panics and hangs must remain
		// visible to the fuzz runner, so do not recover them here.
		_, _ = g.ReadAll(src)
	})
}

func FuzzReadStringRoundTrip(f *testing.F) {
	for _, text := range []string{
		"", "plain", "héllo 世界 😀", "\"quoted\" \\ slash\n\t\r",
		"#. (error \"only text\")", "\x01\x7f", strings.Repeat("x", 2048),
	} {
		f.Add(text)
	}
	f.Fuzz(func(t *testing.T, text string) {
		// The printer walks UTF-8 runes. NUL has legacy string-storage
		// semantics, so neither invalid UTF-8 nor NUL is a roundtrip input.
		if len(text) > 2048 || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			t.Skip()
		}
		g := NewInterp()
		defer runtime.KeepAlive(g)
		encoded := WriteStringOf(g.NewString(text))
		if len(encoded) > 2048 {
			t.Skip()
		}
		// NewString guarantees #. is inside a quoted, escaped string here.
		got, err := g.Read(encoded)
		if err != nil {
			t.Fatalf("Read(%q): %v", encoded, err)
		}
		if !IsString(got) || GoString(got) != text {
			t.Fatalf("roundtrip %q: got %s", text, WriteStringOf(got))
		}
	})
}
