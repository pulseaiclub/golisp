package plugin

import (
	"errors"
	"reflect"
	"testing"

	"golisp/interp"
)

type testPlugin struct {
	closed *[]string
}

func (p testPlugin) Name() string { return "demo" }
func (p testPlugin) Setup(h *PluginHost) error {
	if err := h.Func("add", func(a, b int) int { return a + b }); err != nil {
		return err
	}
	if err := h.Value("answer", 41); err != nil {
		return err
	}
	return h.OnClose(func() error {
		*p.closed = append(*p.closed, "plugin")
		return nil
	})
}

func TestPluginHost(t *testing.T) {
	g := interp.NewInterp()
	var closed []string
	h, err := Load(g, testPlugin{closed: &closed})
	if err != nil {
		t.Fatal(err)
	}
	if h.Name() != "demo" {
		t.Fatalf("name = %q", h.Name())
	}
	v, err := h.Eval(`(demo.add demo.answer 1)`)
	if err != nil || interp.Fixnum(v) != 42 {
		t.Fatalf("plugin eval = %v, err=%v", v, err)
	}
	symAdd := interp.SymID(interp.Symbol("add"))
	if _, ok := g.Lookup(symAdd); ok {
		t.Fatal("plugin export leaked into unqualified namespace")
	}
	if err := h.OnClose(func() error {
		closed = append(closed, "extra")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	symQualified := interp.SymID(interp.Symbol("demo.add"))
	if _, ok := g.Lookup(symQualified); ok {
		t.Fatal("plugin export survived Close")
	}
	if got := reflect.ValueOf(closed).Interface(); !reflect.DeepEqual(got, []string{"extra", "plugin"}) {
		t.Fatalf("cleanup order = %v", got)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Eval(`1`); err == nil {
		t.Fatal("closed host accepted eval")
	}
}

func TestPluginStateIsIsolated(t *testing.T) {
	first := interp.NewInterp()
	second := interp.NewInterp()
	h, err := Load(first, testPlugin{closed: new([]string)})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if v, err := h.Eval(`(demo.add demo.answer 1)`); err != nil || interp.Fixnum(v) != 42 {
		t.Fatalf("first interp eval = %v, err=%v", v, err)
	}
	if _, err := second.EvalString(`demo.answer`); err == nil {
		t.Fatal("plugin binding leaked to second interpreter")
	}
}

func TestPluginSetupFailureClosesHost(t *testing.T) {
	g := interp.NewInterp()
	called := false
	p := PluginFunc{
		PluginName: "broken",
		SetupFunc: func(h *PluginHost) error {
			if err := h.OnClose(func() error {
				called = true
				return nil
			}); err != nil {
				return err
			}
			return errors.New("setup failed")
		},
	}
	if _, err := Load(g, p); err == nil || !called {
		t.Fatalf("setup failure err=%v cleanup=%v", err, called)
	}
}
