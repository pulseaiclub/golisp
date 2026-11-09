package interp

import (
	_ "embed"
)

// Macro definitions kept in the standard library. They are deliberately
// small; expansion happens before bytecode generation.

//go:embed macros.lsp
var macroLibrary string

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
