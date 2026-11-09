package main

import (
	"fmt"
	"os"
	"strings"

	"golisp/interp"
)

// Person is a demo Go struct exposed to Lisp.
type Person struct {
	Name string
	Age  int
	Tags map[string]string
}

func (p *Person) Birthday() int { p.Age++; return p.Age }

func (p *Person) Greet(prefix string) string {
	return prefix + ", " + p.Name
}

func Add(a, b int) int { return a + b }

func main() {
	g := interp.NewInterp()

	// One call per Go entity. No name given: it is derived from Go.
	g.Def("", Add)
	g.Def("p", &Person{Name: "Ada", Age: 36, Tags: map[string]string{"kind": "demo"}})
	g.Import("str", strings.ToUpper, strings.ToLower)

	if len(os.Args) > 1 {
		if err := g.LoadFile(os.Args[1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	script := `
(princ (Add 20 22) "\n")
(princ p.Name " " p.Age "\n")
(set! p.Age 40)
(princ (p.Greet "hi") "\n")
(princ p.Tags.kind "\n")
(princ (str.ToUpper "quiet") "\n")
(princ (str.ToLower "LOUD") "\n")
`
	if _, err := g.EvalString(script); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
