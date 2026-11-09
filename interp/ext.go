package interp

// Extension API: the narrow set of hooks a package outside interp needs to
// add primitives to an interpreter. golisp/tcp is the reference user.
//
//   - DefBuiltin installs a builtin, replacing any previous binding.
//   - Raise raises a Lisp condition from Go code.
//   - ToInt coerces a numeric argument.
//   - (*Interp).NewStream wraps Go I/O handles as a Lisp stream value.
//
// The rest of the surface an extension typically touches is already
// exported: GoString, IsNil, FixnumValue, Symbol/SymID for error kinds,
// and the As* accessors for boxed values.

// DefBuiltin registers a primitive builtin, replacing any previous binding
// for name. nargs follows the same convention as the core builtins: an exact
// argument count, or a negative number meaning "at least -n" (-1 any count,
// -2 at least two, ...).
func (g *Interp) DefBuiltin(name string, nargs int, f func(g *Interp, args []Value) Value) {
	b := &Builtin{name: name, f: f, nargs: nargs}
	v := g.box(boxBuiltin{b})
	id := internString(name)
	g.ensureSymbolState(id)
	g.globals[id] = v
	g.builtins[id] = b
	g.constants[id] = true
}

// Raise raises a Lisp condition with the given error-kind symbol id. Kind
// ids come from SymID(Symbol(name)); the standard categories are listed in
// doc/syntax.md (io-error, type-error, ...).
func Raise(kind uint32, format string, args ...interface{}) {
	raise(kind, format, args...)
}

// ToInt coerces a numeric argument to int64, raising a Lisp condition when
// the value is not a number. what names the builtin for the error message.
func ToInt(v Value, what string) int64 { return toInt(v, what) }
