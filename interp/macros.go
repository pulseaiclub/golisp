package interp

import (
	_ "embed"
)

// Macro definitions kept in the standard library. They are deliberately
// small; expansion happens before bytecode generation.

//go:embed macros.lsp
var macroLibrary string

func (g *Interp) loadMacroLibrary() error {
	previous, sp := g.source, g.sp
	defer func() { g.source = previous; g.sp = sp }()
	forms, locations, err := g.readLocated(macroLibrary, "macros.lsp")
	if err != nil {
		return err
	}
	for _, f := range forms {
		g.push(f)
	}
	for i, f := range forms {
		g.source = locations[i]
		if _, err := g.EvalTop(f); err != nil {
			return err
		}
	}
	return nil
}
