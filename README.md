# golisp

A bytecode-compiling Lisp interpreter and virtual machine written in Go. The
dialect follows `../femtolisp`.

## What is implemented

- NaN-boxed `Value` representation: fixnums, doubles, symbols, characters,
  booleans, and the empty list.
- Chunked private heap with a mark/sweep collector, cons cells, vectors,
  strings, tables, and boxed runtime objects.
- Reader: lists, dotted pairs, quote, quasiquote, comma, strings, characters,
  vectors, and comments.
- Bytecode compiler and VM: lexical variables, closure capture, tail calls,
  recursion, `if`, `begin`, `let`, `while`, `dotimes`, `and`, `or`, `quote`,
  `set!`, `define`, and macros.
- Go primitives and a Lisp standard library covering arithmetic, lists,
  vectors, strings, tables, `apply`, `map`, `filter`, folds, and I/O.
- Go interop through a single `Def` entry point (see below).
- A namespaced `PluginHost` API for embeddable Go plugins.
- File and TCP streams, plus a small set of I/O helpers in the standard library.

## Build and run

```sh
go build -o golisp ./cmd/golisp
echo '(princ (+ 1 2) "\n")' >/tmp/example.lsp
./golisp /tmp/example.lsp
```

Run the tests:

```sh
go test ./...
```

## I/O and networking

Streams are plain values, so they can be passed to functions and stored like
anything else. A stream may be readable, writable, or both.

```lisp
;; files
(write-file "/tmp/a.txt" "hello")
(read-file "/tmp/a.txt")                 ; => "hello"
(file-exists? "/tmp/a.txt")              ; => #t
(delete-file "/tmp/a.txt")

(define s (open "/tmp/a.txt" :write))    ; :read :write :append :read-write :new
(write-line s "line")
(close s)

(define in (open "/tmp/a.txt"))          ; :read is the default
(read-line in)                           ; => "line", then eof-object
(read-all in)
(read-n in 4)
(close in)

;; standard streams
(write-line *stdout* "to stdout")
(read-line *stdin*)
```

Convenience wrappers are defined in Lisp on top of those primitives:
`read-lines`, `file->string`, `string->file`, `with-file`, `call-with-stream`.

TCP uses the same stream object, so once a connection is accepted the read and
write primitives are identical.

```lisp
;; server
(define ln (tcp-listen "127.0.0.1" 8080))
(define conn (tcp-accept ln))
(write-line conn (string "you said: " (read-line conn)))
(close conn)
(close ln)

;; client
(define c (tcp-connect "127.0.0.1" 8080))
(write-line c "ping")
(read-line c)                            ; => "you said: ping"
(close c)
```

Passing `0` as the port binds an ephemeral port; `(listener-port ln)` reports
the real one. Every stream error (missing file, closing twice, reading a
write-only handle, connection refused) is raised as a Lisp condition rather
than crashing the process.

HTTP is not built in. Register `net/http` with `Def` when you need it:

```go
g.Def("http-get", func(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
})
```

## Plugin host

For applications that embed Lisp as a plugin language, use `PluginHost` rather
than scattering direct registrations through the application:

```go
type MetricsPlugin struct{}

func (MetricsPlugin) Name() string { return "metrics" }

func (MetricsPlugin) Setup(h *golisp.PluginHost) error {
	if err := h.Func("add", func(a, b int) int { return a + b }); err != nil {
		return err
	}
	if err := h.Value("version", "1.0"); err != nil {
		return err
	}
	return h.OnClose(func() error {
		// release plugin resources here
		return nil
	})
}

g := golisp.NewInterp()
h, err := g.LoadPlugin(MetricsPlugin{})
if err != nil {
	return err
}
defer h.Close()
```

Exports are automatically namespaced:

```lisp
(metrics.add 20 22)
metrics.version
```

`PluginHost` provides `Func`, `Value`, `Import`, `Eval`, `OnClose`, and `Close`.
Cleanup callbacks run in reverse registration order and `Close` is idempotent.
A plugin setup failure automatically closes the host and runs registered cleanup
callbacks.

The host namespace prevents accidental name collisions. It is not a security
sandbox: a plugin shares the interpreter and can see other registered globals.
Use a separate `golisp.Interp` when state isolation is required. Do not expose
filesystem, network, process, or reflection capabilities to untrusted plugins
unless the host deliberately registers them.

## Go interop

Register anything with one call. When the name is empty it is derived from Go,
so `Add` becomes `Add` and `*Person` becomes `Person`.

```go
type Person struct {
	Name string
	Age  int
	Tags map[string]string
}

func (p *Person) Greet(prefix string) string { return prefix + ", " + p.Name }
func (p *Person) Birthday() int              { p.Age++; return p.Age }
func Add(a, b int) int                        { return a + b }

g := golisp.NewInterp()
g.Def("", Add)                                  // => Add
g.Def("p", &Person{Name: "Ada", Age: 36})       // => p
g.Import("str", strings.ToUpper, strings.ToLower)
```

Lisp then uses ordinary dotted syntax. There are no special forms to learn:

```lisp
(Add 20 22)              ; => 42        call a registered function
p.Name                   ; => "Ada"     read a field
p.Age                    ; => 36
(set! p.Age 40)          ;             write a field
(p.Greet "hi")           ; => "hi, Ada" call a method
p.Birthday               ; => 41        zero-argument method, read as a value
p.Tags.kind              ; => "demo"    nested access, including map entries
(str.ToUpper "quiet")    ; => "QUIET"
```

A dotted name is only treated as a member access when its first component
holds a registered Go value. Otherwise it keeps its normal meaning, so library
names such as `string.length` are unaffected.

Conversions:

| Go | Lisp |
|---|---|
| `bool`, `string`, integer and float kinds | boolean, string, fixnum/double |
| slice, array | vector |
| map | table |
| multiple return values | list |
| trailing `error` return | raised as a Lisp condition |
| struct, pointer, func, channel | boxed Go value |

Arguments convert in the other direction, so `(set! p.Age 40)` and
`(p.Greet "hi")` need no annotations. Only explicitly registered symbols are
visible; Lisp cannot import arbitrary Go packages.

## Performance

The `perf.lsp` suite in `../femtolisp` is the reference workload, but there is
no checked-in Go benchmark harness yet, so no speedup claim is made here.
Measured on an Apple M4 against `femtolisp` for three shared workloads, this
implementation is roughly 2.3x to 3.1x slower.
