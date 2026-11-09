package golisp

// Macro definitions kept in the standard library. They are deliberately
// small; expansion happens before bytecode generation.
const macroLibrary = `
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
`

func (g *Interp) loadMacroLibrary() error {
	forms, err := g.ReadAll(macroLibrary)
	if err != nil {
		return err
	}
	for _, f := range forms {
		if _, err := g.EvalTop(f); err != nil {
			return err
		}
	}
	return nil
}
