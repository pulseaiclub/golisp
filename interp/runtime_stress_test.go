package interp

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRuntimeStressGC(t *testing.T) {
	for _, scenario := range []string{"task closures", "modules and errors"} {
		t.Run(scenario, func(t *testing.T) {
			g := NewInterp()
			defer runtime.KeepAlive(g)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			// Workers inherit this callback. Collect only the interpreter that
			// invokes it, never the parent captured by the test.
			g.Def("stress-gc!", func(current *Interp, _ []Value) Value {
				current.gc()
				runtime.GC()
				return Nil
			})
			eval := func(src string) Value {
				t.Helper()
				v, err := g.EvalStringContext(ctx, src)
				if err != nil {
					t.Fatal(err)
				}
				return v
			}

			if scenario == "modules and errors" {
				dir := t.TempDir()
				writeModule(t, dir, "stress.lsp", `
					(define private-text "module payload")
					(define (make-reader n) (lambda () (stress-gc!) (list private-text n)))
					(define (fail) (stress-gc!) (error "stress failure"))
					(export make-reader)
					(export fail)`)
				g.Def("stress-module-path", g.NewString(filepath.Join(dir, "stress.lsp")))
				eval(`(: stress-module (import stress-module-path))`)
			}

			for i := 0; i < 20; i++ {
				g.Def("stress-index", FixnumValue(int64(i)))
				if scenario == "task closures" {
					eval(`
						(: stress-task (par
						  (let ((payload (list "retained" stress-index)))
						    (stress-gc!)
						    (list payload (lambda ()
						      (stress-gc!)
						      (set-car! (cdr payload) (+ (cadr payload) 1))
						      payload)))))
						(stress-gc!)
						(: stress-result (wait stress-task))
						(set! stress-task #f)
						(stress-gc!)
						((cadr stress-result))`)
					got := eval(`(list (car stress-result) ((cadr stress-result)))`)
					want := fmt.Sprintf(`(("retained" %d) ("retained" %d))`, i+2, i+2)
					if text := WriteStringOf(got); text != want {
						t.Fatalf("iteration %d: got %s, want %s", i, text, want)
					}
					eval(`(set! stress-result #f)`)
				} else {
					eval(`
						(: stress-reader (wait (par (stress-module.make-reader stress-index))))
						(: stress-failure (par (stress-module.fail)))
						(stress-gc!)`)
					if _, err := g.EvalStringContext(ctx, `(wait stress-failure)`); err == nil || !strings.Contains(err.Error(), "stress failure") {
						t.Fatalf("iteration %d: task error = %v", i, err)
					}
					got := eval(`
						(trycatch (lambda () (wait stress-failure))
						          (lambda (e) (stress-gc!) (stress-reader)))`)
					want := fmt.Sprintf(`("module payload" %d)`, i)
					if text := WriteStringOf(got); text != want {
						t.Fatalf("iteration %d: recovery = %s, want %s", i, text, want)
					}
					eval(`(set! stress-reader #f) (set! stress-failure #f)`)
				}
			}
		})
	}
}
