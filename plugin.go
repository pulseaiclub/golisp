package golisp

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// Plugin is the small contract implemented by an embeddable Lisp plugin.
// Setup is called once while the plugin is loaded; it should register its Go
// API and may add cleanup callbacks through PluginHost.OnClose.
type Plugin interface {
	Name() string
	Setup(*PluginHost) error
}

// PluginFunc adapts a setup function to Plugin.
type PluginFunc struct {
	PluginName string
	SetupFunc  func(*PluginHost) error
}

func (p PluginFunc) Name() string { return p.PluginName }
func (p PluginFunc) Setup(h *PluginHost) error {
	if p.SetupFunc == nil {
		return errors.New("nil plugin setup function")
	}
	return p.SetupFunc(h)
}

// PluginHost is the restricted, namespaced host API exposed to a plugin.
// Registration returns errors instead of panicking, and every name is placed
// under the plugin namespace unless it is already qualified.
type pluginExport struct {
	id        uint32
	previous  Value
	installed Value
	bound     bool
}

type PluginHost struct {
	interp  *Interp
	name    string
	mu      sync.Mutex
	closed  bool
	cleanup []func() error
	exports []pluginExport
	seen    map[uint32]bool
}

// NewPluginHost creates a namespaced host for one plugin.
func (g *Interp) NewPluginHost(name string) *PluginHost {
	name = strings.TrimSpace(name)
	name = strings.Trim(name, ".")
	return &PluginHost{interp: g, name: name, seen: make(map[uint32]bool)}
}

// LoadPlugin creates a host, runs Setup, and closes it if setup fails.
func (g *Interp) LoadPlugin(p Plugin) (*PluginHost, error) {
	if p == nil {
		return nil, errors.New("nil plugin")
	}
	h := g.NewPluginHost(p.Name())
	if err := p.Setup(h); err != nil {
		_ = h.Close()
		return nil, fmt.Errorf("plugin %q setup: %w", p.Name(), err)
	}
	return h, nil
}

func (h *PluginHost) qualified(name string) (string, error) {
	if h == nil || h.interp == nil {
		return "", errors.New("nil plugin host")
	}
	name = strings.TrimSpace(name)
	name = strings.Trim(name, ".")
	if name == "" {
		return "", errors.New("empty plugin export name")
	}
	if h.name == "" || strings.HasPrefix(name, h.name+".") {
		return name, nil
	}
	return h.name + "." + name, nil
}

func validatePluginFunction(fn any) error {
	if fn == nil {
		return errors.New("nil Go function")
	}
	rv := reflect.ValueOf(fn)
	if rv.Kind() != reflect.Func {
		return fmt.Errorf("expected Go function, got %s", rv.Type())
	}
	return nil
}

// Func exports a Go function under the plugin namespace.
func (h *PluginHost) bind(name string, value any) error {
	id := internString(name)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return errors.New("plugin host is closed")
	}
	seen := h.seen[id]
	var previous Value
	var bound bool
	if !seen {
		previous, bound = h.interp.symbolValue(id)
	}
	h.mu.Unlock()

	installed := h.interp.Def(name, value)

	h.mu.Lock()
	if !seen && !h.seen[id] {
		h.exports = append(h.exports, pluginExport{
			id: id, previous: previous, installed: installed, bound: bound,
		})
		h.seen[id] = true
	}
	h.mu.Unlock()
	return nil
}

func (h *PluginHost) Func(name string, fn any) error {
	if err := validatePluginFunction(fn); err != nil {
		return err
	}
	qualified, err := h.qualified(name)
	if err != nil {
		return err
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return errors.New("plugin host is closed")
	}
	return h.bind(qualified, fn)
}

// Value exports a Go object, map, scalar, or an existing Lisp value.
func (h *PluginHost) Value(name string, value any) error {
	qualified, err := h.qualified(name)
	if err != nil {
		return err
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return errors.New("plugin host is closed")
	}
	return h.bind(qualified, value)
}

// Import exports several Go functions under a plugin namespace. Unlike the
// legacy Interp.Import, it does not create unqualified aliases.
func (h *PluginHost) Import(prefix string, fns ...any) error {
	if prefix == "" {
		return errors.New("empty import prefix")
	}
	for _, fn := range fns {
		if err := validatePluginFunction(fn); err != nil {
			return err
		}
		short := funcName(reflect.ValueOf(fn))
		name := prefix + "." + short
		if err := h.Func(name, fn); err != nil {
			return err
		}
	}
	return nil
}

// Eval evaluates plugin Lisp source in the shared interpreter.
func (h *PluginHost) Eval(source string) (Value, error) {
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return Nil, errors.New("plugin host is closed")
	}
	return h.interp.EvalString(source)
}

// OnClose adds a cleanup callback. Callbacks execute in reverse registration
// order, matching resource acquisition order.
func (h *PluginHost) OnClose(fn func() error) error {
	if fn == nil {
		return errors.New("nil plugin cleanup function")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errors.New("plugin host is closed")
	}
	h.cleanup = append(h.cleanup, fn)
	return nil
}

// Close releases plugin resources. It is idempotent; all cleanup callbacks
// run even if one fails, and errors are joined for the caller.
func (h *PluginHost) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	cleanup := append([]func() error(nil), h.cleanup...)
	exports := append([]pluginExport(nil), h.exports...)
	h.cleanup = nil
	h.exports = nil
	h.mu.Unlock()

	for i := len(exports) - 1; i >= 0; i-- {
		ex := exports[i]
		current, _ := h.interp.symbolValue(ex.id)
		if current != ex.installed {
			continue // another owner replaced this export after registration
		}
		if ex.bound {
			h.interp.setSymbol(ex.id, ex.previous)
		} else {
			h.interp.setSymbol(ex.id, 0)
		}
	}

	var errs []error
	for i := len(cleanup) - 1; i >= 0; i-- {
		if err := cleanup[i](); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Name returns the namespace assigned to the plugin.
func (h *PluginHost) Name() string {
	if h == nil {
		return ""
	}
	return h.name
}
