
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
