package golisp

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

func TestStringStorageSurvivesRepeatedCollection(t *testing.T) {
	g := NewInterp()
	defer runtime.KeepAlive(g)
	for i := 0; i < 20; i++ {
		_ = g.NewString(strings.Repeat("dead payload", 500))
		g.gc()
		g.gc()
		v := g.NewString("live")
		g.Def("retained-string", v)
		g.gc()
		if got := GoString(v); got != "live" {
			t.Fatalf("collection %d: got %q", i, got)
		}
	}
}

func TestManagedStringStorageAcrossVMsAndGC(t *testing.T) {
	first := NewInterp()
	second := NewInterp()
	defer runtime.KeepAlive(first)
	defer runtime.KeepAlive(second)

	large := strings.Repeat("ab😀", 1<<16)
	values := []struct {
		name string
		text string
	}{
		{"empty", ""},
		{"unicode", "héllo 世界 👋"},
		{"large", large},
	}

	firstValues := make([]Value, len(values))
	secondValues := make([]Value, len(values))
	for i, test := range values {
		firstValues[i] = first.NewString(test.text)
		secondValues[i] = second.NewString(test.text)
		first.stack[first.sp] = firstValues[i]
		first.sp++
		second.stack[second.sp] = secondValues[i]
		second.sp++
	}

	first.gc()
	second.gc()

	for i, test := range values {
		for vm, value := range map[string]Value{
			"first":  firstValues[i],
			"second": secondValues[i],
		} {
			if got := GoString(value); got != test.text {
				t.Fatalf("%s VM %s GoString length = %d, want %d", test.name, vm, len(got), len(test.text))
			}
			want := []byte(test.text)
			got := StringBytes(value)
			if !bytes.Equal(got, want) {
				t.Fatalf("%s VM %s StringBytes mismatch", test.name, vm)
			}
			if len(got) > 0 {
				got[0] ^= 0xff
				if GoString(value) != test.text {
					t.Fatalf("%s VM %s StringBytes aliases managed storage", test.name, vm)
				}
			}
		}
	}
}
