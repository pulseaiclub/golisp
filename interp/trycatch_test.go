package interp

import "testing"

// trycatch is compiled as two operands the VM applies itself: body must be a
// zero-argument thunk, and the handler receives the error object. These cases
// pin the stack discipline when trycatch is nested inside another call.
func TestTryCatch(t *testing.T) {
	g := NewInterp()
	if _, err := g.EvalString(`(define z 0)`); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		src  string
		want string
	}{
		{`(trycatch (lambda () (/ 1 z)) (lambda (e) 'handled))`, "handled"},
		{`(trycatch (lambda () (+ 1 2)) (lambda (e) 'handled))`, "3"},
		{`(trycatch (lambda () (car 5)) (lambda (e) (eq? (car e) 'type-error)))`, "#t"},
		{`(princ (trycatch (lambda () (/ 1 z)) (lambda (e) 7)) "\n")`, "()"},
		{`(+ 1 (trycatch (lambda () (/ 1 z)) (lambda (e) 41)))`, "42"},
		{`(list 1 (trycatch (lambda () (/ 1 z)) (lambda (e) 2)) 3)`, "(1 2 3)"},
		{`(define r (trycatch (lambda () (/ 1 z)) (lambda (e) 'd))) r`, "d"},
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
