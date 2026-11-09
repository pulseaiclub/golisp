package interp

// Dynamic modules (JS-style import/export).
//
// A module is a file loaded through (import "path"). Each module owns a
// private global namespace: defines inside the module go to m.globals, while
// free references fall back to the interpreter's ambient globals (builtins,
// standard library, and host-registered values). Compiled functions carry a
// pointer to their module, so exported functions keep resolving their free
// names in the module namespace no matter who calls them later.

type Module struct {
	Path     string // absolute path, also the cache key
	Dir      string // directory of the module file, for nested imports
	Exports  *Table // values exported to importers, keyed by symbol
	exportsV Value  // boxed Exports; kept as a GC root
	loading  bool   // true from the first import until evaluation finishes

	globals []Value // module-private bindings by symbol id
	macros  []Value // module-private macros by symbol id
}

// growFor ensures the module arrays can hold id.
func (m *Module) growFor(id uint32) {
	n := int(id) + 1
	if len(m.globals) < n {
		m.globals = append(m.globals, make([]Value, n-len(m.globals))...)
	}
	if len(m.macros) < n {
		m.macros = append(m.macros, make([]Value, n-len(m.macros))...)
	}
}

func (m *Module) setGlobal(id uint32, v Value) {
	m.growFor(id)
	m.globals[id] = v
}

func (m *Module) setMacro(id uint32, fn Value) {
	m.growFor(id)
	m.macros[id] = fn
	m.globals[id] = fn
}

// get returns the module-private binding for id, or 0 when unbound.
func (m *Module) get(id uint32) Value {
	if int(id) < len(m.globals) {
		return m.globals[id]
	}
	return 0
}

// getMacro returns the module-private macro binding for id, or 0.
func (m *Module) getMacro(id uint32) Value {
	if int(id) < len(m.macros) {
		return m.macros[id]
	}
	return 0
}

// markModule marks a module's live values for GC.
func (g *Interp) markModule(m *Module) {
	for _, v := range m.globals {
		g.mark(v)
	}
	for _, v := range m.macros {
		g.mark(v)
	}
	if m.exportsV != 0 {
		g.mark(m.exportsV)
	}
}

// envGlobalsOf returns the namespace a function's global references resolve
// against: the module namespace for module functions, nil (the interpreter's
// ambient globals) otherwise.
func envGlobalsOf(fn *Function) []Value {
	if fn.env == nil {
		return nil
	}
	return fn.env.globals
}

// envSetGlobal implements opSetG for the calling function's namespace.
func (g *Interp) envSetGlobal(fn *Function, id uint32, v Value) {
	if fn.env != nil {
		fn.env.setGlobal(id, v)
		return
	}
	g.setGlobal(id, v)
}
