package interp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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

	// Returned async closures use an explicit ambient fallback. A sealed
	// namespace ends lookup even for absent bindings; it never sees the receiver.
	fallback *Module
	sealed   bool
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
	if m == nil || g.gcModules[m] {
		return
	}
	g.gcModules[m] = true
	g.markModule(m.fallback)
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

// envSetGlobal implements opSetG for the calling function's namespace.
func (g *Interp) envSetGlobal(fn *Function, id uint32, v Value) {
	if fn.env != nil {
		fn.env.setGlobal(id, v)
		return
	}
	g.setGlobal(id, v)
}

// currentModule returns the module currently being loaded, if any.
func (g *Interp) currentModule() *Module {
	if n := len(g.modStack); n > 0 {
		return g.modStack[n-1]
	}
	return nil
}

// dynLookup resolves id in the dynamic current namespace: the module being
// loaded, else the ambient globals. Used by builtins such as bound? that
// have no calling function to inherit a namespace from.
func (g *Interp) dynLookup(id uint32) (Value, bool) {
	if m := g.currentModule(); m != nil {
		if v := m.get(id); v != 0 {
			return v, true
		}
	}
	return g.symbolValue(id)
}

// dynSet binds id in the dynamic current namespace.
func (g *Interp) dynSet(id uint32, v Value) {
	if m := g.currentModule(); m != nil {
		m.setGlobal(id, v)
		return
	}
	g.setGlobal(id, v)
}

// envValue reads through the live namespace pointers, so a nested call can
// grow a namespace without leaving its caller with a stale globals slice.
// Ordinary modules fall back to g; retained task namespaces end at sealed.
func envValue(env *Module, g *Interp, id uint32) Value {
	for m := env; m != nil; m = m.fallback {
		if v := m.get(id); v != 0 {
			return v
		}
		if m.sealed {
			return 0
		}
	}
	if int(id) < len(g.globals) {
		return g.globals[id]
	}
	return 0
}

// currentDir returns the directory of the innermost file being loaded, for
// resolving relative import paths. Empty when nothing is being loaded.
func (g *Interp) currentDir() string {
	if n := len(g.fileStack); n > 0 {
		return filepath.Dir(g.fileStack[n-1])
	}
	return ""
}

// resolveModulePath resolves a module spec against the directory of the
// innermost loading file (or the process working directory). A spec without
// an extension also tries the .lsp suffix, like require() trying .js.
func (g *Interp) resolveModulePath(spec string) (string, error) {
	var tried []string
	var dirs []string
	if filepath.IsAbs(spec) {
		dirs = []string{""}
	} else {
		if dir := g.currentDir(); dir != "" {
			dirs = append(dirs, dir)
		}
		dirs = append(dirs, "")
	}
	for _, dir := range dirs {
		cands := []string{filepath.Join(dir, spec)}
		if filepath.Ext(spec) == "" {
			cands = append(cands, filepath.Join(dir, spec)+".lsp")
		}
		for _, cand := range cands {
			cand = filepath.Clean(cand)
			tried = append(tried, cand)
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				abs, err := filepath.Abs(cand)
				if err != nil {
					abs = cand
				}
				return abs, nil
			}
		}
	}
	return "", fmt.Errorf("could not find module %q (tried %s)",
		spec, strings.Join(tried, ", "))
}

// importModule loads a module and returns its exports table. Modules are
// cached by absolute path and evaluated once; a module registered but still
// loading is returned with its partial exports, which is how circular
// imports degrade (CommonJS-style).
func (g *Interp) importModule(spec string) Value {
	path, err := g.resolveModulePath(spec)
	if err != nil {
		raise(symIOError, "import: %s", err.Error())
	}
	if m, ok := g.modules[path]; ok {
		return m.exportsV
	}
	data, err := os.ReadFile(path)
	if err != nil {
		raise(symIOError, "import: could not open %q", path)
	}
	forms, err := g.ReadAll(string(data))
	if err != nil {
		raise(symParseError, "import %q: %s", spec, err.Error())
	}

	m := &Module{
		Path:    path,
		Dir:     filepath.Dir(path),
		Exports: NewTable(),
	}
	m.exportsV = g.box(boxTable{m.Exports})
	if g.modules == nil {
		g.modules = make(map[string]*Module)
	}
	g.modules[path] = m
	g.modStack = append(g.modStack, m)
	g.fileStack = append(g.fileStack, path)
	m.loading = true
	defer func() {
		m.loading = false
		g.modStack = g.modStack[:len(g.modStack)-1]
		g.fileStack = g.fileStack[:len(g.fileStack)-1]
		if r := recover(); r != nil {
			// A failed load must not stay cached as a half-initialized
			// module; the next import may retry.
			delete(g.modules, path)
			panic(r)
		}
	}()
	for _, f := range forms {
		if _, err := g.EvalTop(f); err != nil {
			if le, ok := err.(*LispError); ok {
				panic(&LispError{kind: le.kind,
					msg: fmt.Sprintf("in module %s: %s", filepath.Base(path), le.msg)})
			}
			panic(err)
		}
	}
	return m.exportsV
}

// --- module builtins ---

// (import "path") evaluates a module file once and returns its exports
// table, like JS's dynamic import() / require().
func bImport(g *Interp, args []Value) Value {
	if len(args) != 1 || !IsString(args[0]) {
		raise(symArgError, "import: expected one path string argument")
	}
	return g.importModule(GoString(args[0]))
}

// (module-import* "path" name ...) binds exported names into the current
// namespace, like import {name, ...} from "path". The import* macro quotes
// the names before calling this.
func bImportStar(g *Interp, args []Value) Value {
	if len(args) < 1 || !IsString(args[0]) {
		raise(symArgError, "import*: expected a path string followed by names")
	}
	spec := GoString(args[0])
	exportsV := g.importModule(spec)
	exports := AsTable(exportsV)
	if exports == nil {
		raise(symError, "import*: module %s returned no exports", spec)
	}
	for _, a := range args[1:] {
		var key Value
		switch {
		case IsSymbol(a):
			key = a
		case IsString(a):
			key = Symbol(GoString(a))
		default:
			raise(symArgError, "import*: names must be symbols or strings")
		}
		v, ok := exports.Get(key)
		if !ok {
			raise(symError, "import*: %s does not export %s", spec, SymName(key))
		}
		g.dynSet(SymID(key), v)
	}
	return exportsV
}

// (module-export name) adds an existing binding to the current module's
// exports. (module-export name value) binds the value in the module first,
// like export const name = value. The export macro quotes the name. Only
// valid while a module is loading.
func bExport(g *Interp, args []Value) Value {
	m := g.currentModule()
	if m == nil {
		raise(symError, "export: only valid inside a module")
	}
	if len(args) < 1 || len(args) > 2 {
		raise(symArgError, "export: expected (export name) or (export name value)")
	}
	var key Value
	switch {
	case IsSymbol(args[0]):
		key = args[0]
	case IsString(args[0]):
		key = Symbol(GoString(args[0]))
	default:
		raise(symArgError, "export: name must be a symbol or string")
	}
	var v Value
	if len(args) == 2 {
		v = args[1]
		m.setGlobal(SymID(key), v)
	} else {
		var ok bool
		v, ok = g.dynLookup(SymID(key))
		if !ok {
			raise(symUnboundError, "export: variable %s has no value", SymName(key))
		}
	}
	m.Exports.Put(key, v)
	return v
}
