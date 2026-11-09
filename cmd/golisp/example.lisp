; example-plugin.lisp
; Run from the repository root:
;   go run ./cmd/golisp ./cmd/golisp/example.lisp
;
; The demo host registers:
;   Add      - a Go function
;   p        - a Go *Person value
;   str.*    - selected functions from Go's strings package

(princ "== Go function ==\n")
(princ "Add(20, 22) = " (Add 20 22) "\n")

(princ "== Go struct fields ==\n")
(princ "name = " p.Name "\n")
(princ "age  = " p.Age "\n")
(princ "tag  = " p.Tags.kind "\n")

(princ "== Go struct mutation ==\n")
(set! p.Age 40)
(princ "updated age = " p.Age "\n")

(princ "== Go methods ==\n")
(princ "greeting = " (p.Greet "hello") "\n")
(princ "birthday property = " p.Birthday "\n")
(princ "birthday call = " (p.Birthday) "\n")

(princ "== Imported Go functions ==\n")
(princ "upper = " (str.ToUpper "quiet") "\n")
(princ "lower = " (str.ToLower "LOUD") "\n")

(princ "== Lisp file IO ==\n")
(define demo-file "/tmp/golisp-plugin-example.txt")
(write-file demo-file "written by Lisp through the Go host\n")
(princ "file exists = " (file-exists? demo-file) "\n")
(princ "file contents = " (read-file demo-file))
(delete-file demo-file)
(princ "file removed = " (not (file-exists? demo-file)) "\n")

(princ "== Lisp standard library ==\n")
(princ "squares = " (map (lambda (x) (* x x)) (list 1 2 3 4)) "\n")
