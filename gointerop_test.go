package golisp

import "testing"

type interopPerson struct {
	Name string
	Age  int
}

func interopAdd(a, b int) int { return a + b }

func (p *interopPerson) Greet(prefix string) string {
	return prefix + ", " + p.Name
}

func TestGoInterop(t *testing.T) {
	g := NewInterp()
	g.RegisterFunc("go-add", interopAdd)
	person := &interopPerson{Name: "Ada", Age: 36}
	g.Register("person", person)
	if v, ok := g.symbolValue(internString("go-add")); !ok || !IsBoxed(v) {
		t.Fatalf("registered function binding is not boxed: %v %v", v, ok)
	} else if _, _, ok := AsGoFunc(v); !ok {
		t.Fatalf("registered function payload is not go func: %v", boxOf(v).data)
	}

	v, err := g.EvalString(`(go-add 20 22)`)
	if err != nil || Fixnum(v) != 42 {
		t.Fatalf("go function: value=%v err=%v", v, err)
	}
	v, err = g.EvalString(`(go.get person 'Name)`)
	if err != nil || GoString(v) != "Ada" {
		t.Fatalf("go get: value=%v err=%v", v, err)
	}
	if _, err = g.EvalString(`(go.set! person 'Age 37)`); err != nil {
		t.Fatal(err)
	}
	if person.Age != 37 {
		t.Fatalf("go.set! did not update object: %d", person.Age)
	}
	v, err = g.EvalString(`(go.call person 'Greet "Hello")`)
	if err != nil || GoString(v) != "Hello, Ada" {
		t.Fatalf("go method: value=%v err=%v", v, err)
	}
}
