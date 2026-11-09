package golisp

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNetworkViaGoInterop documents the supported way to give Lisp network
// access today: register a Go function, call it from Lisp. The trailing error
// return is raised as a Lisp condition, as shown by the second sub-test.
func TestNetworkViaGoInterop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hello":
			fmt.Fprint(w, "hello from server")
		default:
			http.Error(w, "nope", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	g := NewInterp()
	g.Def("http-get", func(url string) (string, error) {
		resp, err := http.Get(url)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("%s: %s", resp.Status, body)
		}
		return string(body), nil
	})

	v, err := g.EvalString(`(http-get "` + srv.URL + `/hello")`)
	if err != nil {
		t.Fatalf("success path: %v", err)
	}
	if got := GoString(v); got != "hello from server" {
		t.Fatalf("body = %q", got)
	}

	if _, err := g.EvalString(`(http-get "` + srv.URL + `/missing")`); err == nil {
		t.Fatal("expected the HTTP error to surface as a Lisp condition")
	} else if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error did not carry the status: %v", err)
	}
}
