package interp

import (
	"fmt"
	"reflect"
	"runtime"
	"strings"
)

// Go interop.
//
// One entry point, g.Def, registers anything:
//
//	g.Def("add", interpAdd)            // function
//	g.Def("person", &Person{...})      // object (fields and methods)
//	g.Def("", interpGreet)             // name derived from Go: "interpGreet"
//
// Lisp then uses ordinary dotted syntax, with no special forms:
//
//	(add 20 22)             ; call a registered function
//	person.Name             ; read a field
//	(set! person.Age 37)    ; write a field
//	(person.Greet "hi")     ; call a method
//	(keys.Max)              ; read a map entry
//
// A dotted name whose first component is not a registered Go value keeps its
// usual meaning, so library names such as string.length still resolve to the
// globals defined in Lisp.

func (g *Interp) Def(name string, v any) Value {
	switch x := v.(type) {
	case Value:
		if name != "" {
			g.setSymbol(internString(name), x)
		}
		return x
	case func(g *Interp, args []Value) Value:
		b := &Builtin{name: nameOr(name, "go"), f: x, nargs: -1}
		v := g.box(boxBuiltin{b})
		if name != "" {
			g.setSymbol(internString(name), v)
		}
		return v
	}
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		if name != "" {
			g.setSymbol(internString(name), Nil)
		}
		return Nil
	}
	if rv.Kind() == reflect.Func {
		if name == "" {
			name = funcName(rv)
		}
		out := g.box(boxGoFunc{v: rv, name: name})
		g.setSymbol(internString(name), out)
		return out
	}
	if name == "" {
		name = typeNameOf(rv)
	}
	out := g.box(boxGoValue{v: rv, name: name})
	g.setSymbol(internString(name), out)
	return out
}

// Import registers Go functions under a shared prefix and, when the short
// name is still free, under the bare name too:
//
//	g.Import("strings", strings.ToUpper, strings.ToLower)
//	; => (strings.ToUpper "x"), (strings.ToLower "X"), (ToUpper "x"), ...
func (g *Interp) Import(prefix string, fns ...any) {
	for _, fn := range fns {
		rv := reflect.ValueOf(fn)
		if !rv.IsValid() || rv.Kind() != reflect.Func {
			panic("Import requires functions")
		}
		short := funcName(rv)
		long := short
		if prefix != "" {
			long = prefix + "." + short
		}
		v := g.box(boxGoFunc{v: rv, name: long})
		g.setSymbol(internString(long), v)
		if _, taken := g.symbolValue(internString(short)); !taken {
			g.setSymbol(internString(short), v)
		}
	}
}

// RegisterFunc, Register and RegisterValue are kept as thin aliases for the
// original API.

func (g *Interp) RegisterFunc(name string, fn any) Value { return g.Def(name, fn) }
func (g *Interp) Register(name string, v any) Value      { return g.Def(name, v) }
func (g *Interp) RegisterValue(name string, v Value)     { g.Def(name, v) }

func nameOr(name, fallback string) string {
	if name == "" {
		return fallback
	}
	return name
}

func funcName(rv reflect.Value) string {
	if f := runtime.FuncForPC(rv.Pointer()); f != nil {
		full := f.Name()
		if i := strings.LastIndexByte(full, '.'); i >= 0 {
			full = full[i+1:]
		}
		full = strings.TrimSuffix(full, "-fm")
		return full
	}
	return rv.Type().String()
}

func typeNameOf(rv reflect.Value) string {
	t := rv.Type()
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Name()
}

// --- value conversion -------------------------------------------------------

func goValueToLisp(g *Interp, v reflect.Value) Value {
	if !v.IsValid() {
		return Nil
	}
	for v.Kind() == reflect.Interface {
		if v.IsNil() {
			return Nil
		}
		v = v.Elem()
	}
	if v.Type() == reflect.TypeOf(Value(0)) {
		return v.Interface().(Value)
	}
	switch v.Kind() {
	case reflect.Bool:
		return BoolValue(v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := v.Int()
		if n >= fixnumMin && n <= fixnumMax {
			return FixnumValue(n)
		}
		return DoubleValue(float64(n))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32,
		reflect.Uint64, reflect.Uintptr:
		n := v.Uint()
		if n <= uint64(fixnumMax) {
			return FixnumValue(int64(n))
		}
		return DoubleValue(float64(n))
	case reflect.Float32, reflect.Float64:
		return DoubleValue(v.Float())
	case reflect.String:
		return g.NewString(v.String())
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return g.NewVector(0, Nil)
		}
		out := g.NewVector(v.Len(), Nil)
		for i := 0; i < v.Len(); i++ {
			VectorSet(out, i, goValueToLisp(g, v.Index(i)))
		}
		return out
	case reflect.Map:
		t := NewTable()
		iter := v.MapRange()
		for iter.Next() {
			t.Put(goValueToLisp(g, iter.Key()), goValueToLisp(g, iter.Value()))
		}
		return g.box(boxTable{t})
	case reflect.Func:
		return g.box(boxGoFunc{v: v, name: v.Type().String()})
	}
	return g.box(boxGoValue{v: v, name: v.Type().String()})
}

func lispToGo(v Value, t reflect.Type) (reflect.Value, error) {
	if t == reflect.TypeOf(Value(0)) {
		return reflect.ValueOf(v), nil
	}
	if v == Nil {
		return reflect.Zero(t), nil
	}
	if rv, _, ok := AsGoValue(v); ok {
		switch {
		case rv.Type().AssignableTo(t):
			return rv, nil
		case rv.Type().ConvertibleTo(t):
			return rv.Convert(t), nil
		}
	}
	if t.Kind() == reflect.Interface {
		return reflect.ValueOf(lispNative(v)), nil
	}
	switch t.Kind() {
	case reflect.Bool:
		if !IsBool(v) {
			return reflect.Value{}, fmt.Errorf("expected boolean")
		}
		r := reflect.New(t).Elem()
		r.SetBool(v == True)
		return r, nil
	case reflect.String:
		r := reflect.New(t).Elem()
		if IsString(v) {
			r.SetString(GoString(v))
		} else {
			r.SetString(DisplayString(v))
		}
		return r, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		r := reflect.New(t).Elem()
		r.SetInt(toInt(v, "Go argument"))
		return r, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32,
		reflect.Uint64, reflect.Uintptr:
		r := reflect.New(t).Elem()
		r.SetUint(uint64(toInt(v, "Go argument")))
		return r, nil
	case reflect.Float32, reflect.Float64:
		r := reflect.New(t).Elem()
		r.SetFloat(toFloat(v))
		return r, nil
	case reflect.Slice:
		if IsVector(v) {
			n := VectorLen(v)
			out := reflect.MakeSlice(t, n, n)
			for i := 0; i < n; i++ {
				ev, err := lispToGo(VectorRef(v, i), t.Elem())
				if err != nil {
					return reflect.Value{}, err
				}
				out.Index(i).Set(ev)
			}
			return out, nil
		}
		return reflect.Value{}, fmt.Errorf("expected vector for %s", t)
	}
	return reflect.Value{}, fmt.Errorf("cannot convert %s to %s", typeName(v), t)
}

func lispNative(v Value) any {
	switch {
	case v == Nil:
		return nil
	case v == True:
		return true
	case v == False:
		return false
	case IsFixnum(v):
		return Fixnum(v)
	case IsDouble(v):
		return Double(v)
	case IsString(v):
		return GoString(v)
	case IsChar(v):
		return Char(v)
	case IsSymbol(v):
		return SymName(v)
	}
	return DisplayString(v)
}

// --- calling Go -------------------------------------------------------------

func callReflect(g *Interp, fn reflect.Value, args []Value, label string) Value {
	if fn.Kind() != reflect.Func {
		raise(symTypeError, "%s: expected Go function", label)
	}
	t := fn.Type()
	if t.IsVariadic() {
		if len(args) < t.NumIn()-1 {
			raise(symArgError, "%s: too few arguments", label)
		}
	} else if len(args) != t.NumIn() {
		raise(symArgError, "%s: expects %d arguments, got %d", label, t.NumIn(), len(args))
	}

	in := make([]reflect.Value, len(args))
	for i, a := range args {
		pt := t.In(i)
		if t.IsVariadic() && i >= t.NumIn()-1 {
			pt = t.In(t.NumIn() - 1).Elem()
		}
		rv, err := lispToGo(a, pt)
		if err != nil {
			raise(symTypeError, "%s argument %d: %s", label, i+1, err)
		}
		in[i] = rv
	}

	var out []reflect.Value
	func() {
		defer func() {
			if r := recover(); r != nil {
				raise(symError, "%s: %v", label, r)
			}
		}()
		out = fn.Call(in)
	}()
	return unpackResults(g, out, label)
}

func unpackResults(g *Interp, out []reflect.Value, label string) Value {
	if len(out) == 0 {
		return Nil
	}
	errType := reflect.TypeOf((*error)(nil)).Elem()
	if len(out) > 1 && out[len(out)-1].Type().Implements(errType) {
		last := out[len(out)-1]
		out = out[:len(out)-1]
		if !last.IsNil() {
			raise(symError, "%s: %s", label, last.Interface().(error).Error())
		}
	}
	if len(out) == 1 {
		return goValueToLisp(g, out[0])
	}
	vals := make([]Value, len(out))
	for i := range out {
		vals[i] = goValueToLisp(g, out[i])
	}
	return buildImproperList(g, vals, Nil)
}

// --- member resolution ------------------------------------------------------

// deref follows pointers and interfaces so fields and map entries are
// reachable from the value the caller registered.
func deref(rv reflect.Value) (reflect.Value, error) {
	for {
		switch rv.Kind() {
		case reflect.Interface, reflect.Ptr:
			if rv.IsNil() {
				return reflect.Value{}, fmt.Errorf("nil Go value")
			}
			rv = rv.Elem()
		default:
			return rv, nil
		}
	}
}

// memberOf resolves obj.member as a field, a bound method, or a map entry.
// The returned value is a reflect.Value that callers either read or call.
func memberOf(obj Value, member string) (reflect.Value, error) {
	rv, _, ok := AsGoValue(obj)
	if !ok {
		return reflect.Value{}, fmt.Errorf("not a Go value")
	}

	// Methods are looked up on the value as registered, before dereferencing,
	// so pointer receivers keep working.
	if m := rv.MethodByName(member); m.IsValid() {
		return m, nil
	}
	if rv.Kind() != reflect.Ptr && rv.CanAddr() {
		if m := rv.Addr().MethodByName(member); m.IsValid() {
			return m, nil
		}
	}

	base, err := deref(rv)
	if err != nil {
		return reflect.Value{}, err
	}

	if base.Kind() == reflect.Map {
		if base.Type().Key().Kind() == reflect.String {
			got := base.MapIndex(reflect.ValueOf(member).Convert(base.Type().Key()))
			if got.IsValid() {
				return got, nil
			}
		}
		return reflect.Value{}, fmt.Errorf("no key %q", member)
	}

	if base.Kind() == reflect.Struct {
		if f, ok := base.Type().FieldByName(member); ok && len(f.Index) > 0 {
			return base.FieldByIndex(f.Index), nil
		}
	}

	// Be forgiving about Go's exported casing.
	for _, alt := range []string{exported(member), unexported(member)} {
		if alt == member {
			continue
		}
		if m := rv.MethodByName(alt); m.IsValid() {
			return m, nil
		}
		if base.Kind() == reflect.Struct {
			if f, ok := base.Type().FieldByName(alt); ok && len(f.Index) > 0 {
				return base.FieldByIndex(f.Index), nil
			}
		}
	}
	return reflect.Value{}, fmt.Errorf("no member %q", member)
}

func exported(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func unexported(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// walkPath resolves a dotted path such as "Tags.kind" one component at a time.
// The last component is returned unresolved so callers can decide whether to
// read, invoke, or assign it.
func walkPath(g *Interp, obj Value, path string) (parent Value, last string, err error) {
	parts := strings.Split(path, ".")
	cur := obj
	for i := 0; i < len(parts)-1; i++ {
		mv, err := memberOf(cur, parts[i])
		if err != nil {
			return 0, "", err
		}
		if mv.Kind() == reflect.Func {
			return 0, "", fmt.Errorf("%s is a method, not a container", parts[i])
		}
		cur = g.box(boxGoValue{v: mv, name: parts[i]})
	}
	return cur, parts[len(parts)-1], nil
}

// goMemberGet reads obj.path. Zero-argument methods are invoked so that
// obj.Size behaves like a property, unless the caller wants a callable.
func goMemberGet(g *Interp, obj Value, path string, callable bool) Value {
	container, member, err := walkPath(g, obj, path)
	if err != nil {
		raise(symError, "go: %s", err)
	}
	mv, err := memberOf(container, member)
	if err != nil {
		raise(symError, "go: %s", err)
	}
	if mv.Kind() != reflect.Func {
		return goValueToLisp(g, mv)
	}
	if callable || mv.Type().NumIn() != 0 {
		return g.box(boxGoFunc{v: mv, name: member})
	}
	var out []reflect.Value
	func() {
		defer func() {
			if r := recover(); r != nil {
				raise(symError, "go.%s: %v", member, r)
			}
		}()
		out = mv.Call(nil)
	}()
	return unpackResults(g, out, "go."+member)
}

// goMemberSet writes obj.path. Map entries are updatable when the map is
// reachable through the registered value.
func goMemberSet(g *Interp, obj Value, path string, val Value) {
	container, member, perr := walkPath(g, obj, path)
	if perr != nil {
		raise(symError, "set!: %s", perr)
	}
	rv, _, ok := AsGoValue(container)
	if !ok {
		raise(symTypeError, "set!: %s is not a Go value", DisplayString(container))
	}
	base, err := deref(rv)
	if err != nil {
		raise(symError, "set!: %s", err)
	}

	if base.Kind() == reflect.Map {
		if base.Type().Key().Kind() != reflect.String {
			raise(symTypeError, "set!: map key must be a string")
		}
		converted, err := lispToGo(val, base.Type().Elem())
		if err != nil {
			raise(symTypeError, "set!.%s: %s", member, err)
		}
		base.SetMapIndex(reflect.ValueOf(member).Convert(base.Type().Key()), converted)
		return
	}

	if base.Kind() != reflect.Struct {
		raise(symTypeError, "set!: %s has no settable member %q", base.Type(), member)
	}
	field := base.FieldByName(member)
	if !field.IsValid() {
		if f, ok := base.Type().FieldByName(exported(member)); ok {
			field = base.FieldByIndex(f.Index)
		}
	}
	if !field.IsValid() || !field.CanSet() {
		raise(symError, "set!: %q is not a settable field", member)
	}
	converted, err := lispToGo(val, field.Type())
	if err != nil {
		raise(symTypeError, "set!.%s: %s", member, err)
	}
	field.Set(converted)
}

// --- the explicit go.* helpers ---------------------------------------------

func bGoGet(g *Interp, args []Value) Value {
	if len(args) != 2 {
		raise(symArgError, "go.get: expected object and member")
	}
	return goMemberGet(g, args[0], memberName(args[1]), false)
}

func bGoSet(g *Interp, args []Value) Value {
	if len(args) != 3 {
		raise(symArgError, "go.set!: expected object, member and value")
	}
	goMemberSet(g, args[0], memberName(args[1]), args[2])
	return args[2]
}

func bGoCall(g *Interp, args []Value) Value {
	if len(args) < 2 {
		raise(symArgError, "go.call: expected object and member")
	}
	container, member, perr := walkPath(g, args[0], memberName(args[1]))
	if perr != nil {
		raise(symError, "go.call: %s", perr)
	}
	obj := container
	mv, err := memberOf(obj, member)
	if err != nil {
		raise(symError, "go.call: %s", err)
	}
	switch mv.Kind() {
	case reflect.Func:
		return callReflect(g, mv, args[2:], "go.call "+member)
	case reflect.Map, reflect.Struct:
		return goMemberGet(g, obj, member, false)
	}
	return goValueToLisp(g, mv)
}

func memberName(v Value) string {
	if IsSymbol(v) {
		return SymName(v)
	}
	return DisplayString(v)
}

func bGoCallable(g *Interp, args []Value) Value {
	if len(args) == 0 {
		raise(symArgError, "go.apply: expected function")
	}
	fn, name, ok := AsGoFunc(args[0])
	if !ok {
		raise(symTypeError, "go.apply: expected registered Go function")
	}
	return callReflect(g, fn, args[1:], name)
}

// --- runtime support for dotted symbols ------------------------------------

// globalOrZero returns a global binding, or 0 when it is unbound.
func (g *Interp) globalOrZero(id uint32) Value {
	g.ensureSymbolState(id)
	return g.globals[id]
}

// GoMember resolves prefix.member, falling back to the global held by full
// when prefix is not a registered Go value. This keeps string.length and
// friends working while allowing person.Name.
func (g *Interp) GoMember(prefix, full Value, member string, callable bool) Value {
	if pv := g.globalOrZero(SymID(prefix)); pv != 0 && IsBoxed(pv) {
		if _, _, ok := AsGoValue(pv); ok {
			return goMemberGet(g, pv, member, callable)
		}
	}
	if v := g.globalOrZero(SymID(full)); v != 0 {
		return v
	}
	raise(symUnboundError, "variable %s has no value", SymName(full))
	return Nil
}

// GoSet assigns prefix.member, falling back to the plain global when prefix
// is not a Go value.
func (g *Interp) GoSet(prefix, full Value, member string, val Value) {
	if pv := g.globalOrZero(SymID(prefix)); pv != 0 && IsBoxed(pv) {
		if _, _, ok := AsGoValue(pv); ok {
			goMemberSet(g, pv, member, val)
			return
		}
	}
	g.setSymbol(SymID(full), val)
}
