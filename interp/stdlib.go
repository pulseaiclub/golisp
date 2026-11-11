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
	previous, sp := g.source, g.sp
	defer func() { g.source = previous; g.sp = sp }()
	forms, locations, err := g.readLocated(standardLibrary, "stdlib.lsp")
	if err != nil {
		return err
	}
	for _, f := range forms {
		g.push(f)
	}
	for i, f := range forms {
		g.source = locations[i]
		if _, err := g.EvalTop(f); err != nil {
			return fmt.Errorf("standard library form %s: %w", WriteStringOf(f), err)
		}
	}
	return nil
}
