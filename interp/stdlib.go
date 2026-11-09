package interp

import (
	_ "embed"
	"fmt"
)

// Lisp standard library, loaded into every interpreter. Keeping the source as
// an embedded file avoids a runtime file dependency while preserving
// femtolisp's familiar surface language.

//go:embed stdlib.lsp
var standardLibrary string

func (g *Interp) loadStandardLibrary() error {
	forms, err := g.ReadAll(standardLibrary)
	if err != nil {
		return err
	}
	for _, f := range forms {
		if _, err := g.EvalTop(f); err != nil {
			return fmt.Errorf("standard library form %s: %w", WriteStringOf(f), err)
		}
	}
	return nil
}
