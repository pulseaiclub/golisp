
(define-macro (when test . body)
  (list 'if test (cons 'begin body) ()))
(define-macro (unless test . body)
  (list 'if (list 'not test) (cons 'begin body) ()))
(define-macro (time expr)
  (list 'let (list (list '_t0 (list 'time.now)))
    (list 'let (list (list '_v expr))
      (list 'begin
        (list 'princ "Elapsed time: " (list '- (list 'time.now) '_t0) " seconds\\n")
        '_v))))

; --- modules ---
; (export name) / (export name value): quote the name, pass the value through.
; The underlying primitive is module-export.
(define-macro (export . args)
  (if (and (pair? args) (symbol? (car args)))
      (if (pair? (cdr args))
          (list 'module-export (list 'quote (car args)) (cadr args))
          (list 'module-export (list 'quote (car args))))
      (cons 'module-export args)))

; (import* "path" name ...): quote every imported name. The underlying
; primitive is module-import*.
(define-macro (import* . args)
  (cons 'module-import*
        (cons (car args)
              (map (lambda (n) (list 'quote n)) (cdr args)))))
