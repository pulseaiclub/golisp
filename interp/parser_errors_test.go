package interp

import (
	"runtime"
	"testing"
)

func TestParserMalformedInput(t *testing.T) {
	for _, src := range []string{
		"#|unterminated", "#|outer #|inner|#", "|unterminated", "#;",
		"#; #; 1", "#; #|unterminated", "#; |unterminated", "#; (a . b",
		"(#;)", "(a #; #; b)", "(a #|unterminated", "(a |unterminated",
		"(a]", "[a)", "(]", "[)", "#(a]",
		"(. a)", "[. a]", "(#; a . b)", "(a . b", "[a . b",
		"(a . b]", "[a . b)", "(a . b c)", "(a .)", "(a .",
		"(a . b #;)", "(a . b #|unterminated", "(a . #; b)",
	} {
		t.Run(src, func(t *testing.T) {
			g := NewInterp()
			defer runtime.KeepAlive(g)
			assertParseError := func(err error) {
				t.Helper()
				le, ok := err.(*LispError)
				if !ok || le.kind != symParseError {
					t.Fatalf("expected parse error, got %v", err)
				}
			}
			_, err := g.Read(src)
			assertParseError(err)
			_, err = g.ReadAll(src)
			assertParseError(err)
			forms, err := g.ReadAll("42 " + src)
			assertParseError(err)
			if len(forms) != 1 || forms[0] != FixnumValue(42) {
				t.Fatalf("ReadAll lost completed forms: %v", forms)
			}
		})
	}
}

func TestParserValidCommentsAndDots(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"#|outer #|inner|# outer|# 42", "42"},
		{"#; #; ignored discarded kept", "kept"},
		{"#; #|nested #|comment|#|# (ignored) kept", "kept"},
		{"(a #; #; ignored discarded b)", "(a b)"},
		{"(a #|nested #|comment|#|# b)", "(a b)"},
		{"(a . b)", "(a . b)"},
		{"[a . b]", "(a . b)"},
		{"(a b . c)", "(a b . c)"},
		{"(a . (b c))", "(a b c)"},
		{"(a . #; ignored b #; ignored)", "(a . b)"},
		{"(a . b #|comment|#)", "(a . b)"},
		{"(a.b .5)", "(a.b 0.5)"},
		{"|symbol with spaces|", "symbol with spaces"},
		{"([a] (b))", "((a) (b))"},
	} {
		t.Run(tc.src, func(t *testing.T) {
			g := NewInterp()
			defer runtime.KeepAlive(g)
			v, err := g.Read(tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if got := WriteStringOf(v); got != tc.want {
				t.Fatalf("Read = %s, want %s", got, tc.want)
			}
			forms, err := g.ReadAll(tc.src + " #|trailing #|nested|#|# #; ignored")
			if err != nil || len(forms) != 1 {
				t.Fatalf("ReadAll: %v, %v", forms, err)
			}
			if got := WriteStringOf(forms[0]); got != tc.want {
				t.Fatalf("ReadAll = %s, want %s", got, tc.want)
			}
		})
	}
	g := NewInterp()
	defer runtime.KeepAlive(g)
	for _, src := range []string{"", "#|outer #|inner|#|#", "#; #; ignored discarded"} {
		if forms, err := g.ReadAll(src); err != nil || len(forms) != 0 {
			t.Fatalf("ReadAll(%q): %v, %v", src, forms, err)
		}
	}
}

func TestParserCommentErrorStaysSticky(t *testing.T) {
	g := NewInterp()
	defer runtime.KeepAlive(g)
	for _, src := range []string{"#|unterminated", "#;", "#; #;", "#; #; ) kept", "#; |unterminated"} {
		r := &reader{src: src, g: g}
		r.skipWS()
		first := r.err
		if first == nil {
			t.Fatalf("skipWS(%q) lost comment error", src)
		}
		pos := r.pos
		r.skipWS()
		if _, err := r.read(); err != first || r.err != first || r.pos != pos {
			t.Fatalf("reader(%q) changed error or position: %v, %v, %d -> %d", src, first, err, pos, r.pos)
		}
	}
}
