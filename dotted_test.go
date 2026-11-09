package golisp

import "testing"

type point struct {
	X, Y int
	Tags map[string]string
}

func (p *point) Move(dx, dy int) { p.X += dx; p.Y += dy }

func (p *point) Sum() int { return p.X + p.Y }

func (p *point) Scale(k int) *point {
	return &point{X: p.X * k, Y: p.Y * k, Tags: p.Tags}
}

func interopSum(a, b int) int { return a + b }

func TestDottedMemberAccess(t *testing.T) {
	g := NewInterp()
	g.Def("", interopSum)
	p := &point{X: 1, Y: 2, Tags: map[string]string{"kind": "demo"}}
	g.Def("p", p)

	cases := []struct {
		src  string
		want string
	}{
		{`(interopSum 20 22)`, "42"},
		{`p.X`, "1"},
		{`(set! p.Y 10) p.Y`, "10"},
		{`(p.Move 5 5) p.X`, "6"},
		{`p.Sum`, "21"},   // zero-arg method read as a value
		{`(p.Sum)`, "21"}, // and called
		{`(p.Scale 2)`, "#<go *golisp.point>"},
		{`p.Tags.kind`, "demo"},        // map entry through a dotted name
		{`(string.length "abc")`, "3"}, // ordinary dotted global still works
	}
	for _, c := range cases {
		v, err := g.EvalString(c.src)
		if err != nil {
			t.Fatalf("%s: %v", c.src, err)
		}
		if got := DisplayString(v); got != c.want {
			t.Fatalf("%s = %s, want %s", c.src, got, c.want)
		}
	}
}

func TestImportRegistersPrefixAndShortName(t *testing.T) {
	g := NewInterp()
	g.Import("mathx", interopSum)
	for _, name := range []string{"mathx.interopSum", "interopSum"} {
		if _, ok := g.symbolValue(internString(name)); !ok {
			t.Fatalf("%s was not registered", name)
		}
	}
	if v, err := g.EvalString(`(mathx.interopSum 1 2)`); err != nil || Fixnum(v) != 3 {
		t.Fatalf("import call: %v %v", v, err)
	}
}
