; Run with: go run ./cmd/golisp cmd/golisp/parallel.lisp
; Spawn coarse computations, then join explicitly.
(define (fib n)
  (if (< n 2) n (+ (fib (- n 1)) (fib (- n 2)))))

(: x (par (fib 24)))
(: y x)
(: z (par (fib 25)))
(princ "Tasks scheduled; parent continues.\n")
(princ "Results: " (wait y) " " (wait z) "\n")
