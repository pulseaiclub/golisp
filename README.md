# golisp

A bytecode-compiling Lisp interpreter and virtual machine written in Go. The
dialect is based on the femtolisp project.

## Layout

```
cmd/golisp/     command-line demo host and example.lisp
interp/         the interpreter library (package interp)
plugin/         optional namespaced plugin-host API (package plugin)
```

Applications import the library as `golisp/interp`:

```go
import "golisp/interp"

g := interp.NewInterp()
```

The reader, compiler, VM, heap, and builtins intentionally live in one
package: they are mutually recursive (`eval` compiles at runtime, macro
expansion calls back into the VM, and the GC walks compiler constants),
and Go forbids cyclic imports.

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
- A namespaced `PluginHost` API for embeddable Go plugins in the optional
  `golisp/plugin` package.
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
g := interp.NewInterp()
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

## Parallel tasks

`par` schedules one expression on an independent VM and returns a task handle.
`wait` explicitly joins the task. Copying or passing a handle never waits:

```lisp
(define (work n) (* n n))
(: x (par (work 20)))       ; : is shorthand for define
(: y x)                    ; the same task, without waiting
(: z (par (work 22)))
(+ (wait y) (wait z))       ; => 884
```

Tasks can capture lexical variables, depend on other tasks with `wait`, and
start nested tasks. `wait` returns one level of result, caches it for repeated
waits, and propagates task errors (Lisp errors can be caught by `trycatch`).
Ordinary functions receive task handles as ordinary values; there is no
implicit waiting. `wait` requires exactly one task.

Each task takes a snapshot of Lisp bindings, closures, modules, and mutable
Lisp objects when it is created. Its VM stack and managed heap are independent,
so tasks can execute on different CPU cores while the parent continues to
compile and evaluate code. Task mutations do not modify the parent's Lisp
state; return values are copied back on `wait`. Later definitions in the parent
are not visible to an existing task. Cycles and aliases within a copied object
graph are preserved. Snapshot creation has a cost proportional to reachable
state: use `par` for substantial work, not individual arithmetic operations.

Returned functions retain their task environment, including lexical captures,
ambient globals, and module fallback bindings. Calling the function after
`wait` does not rebind its global names to the receiver. Functions returned in
one result graph share their copied environment; their mutations stay local
to that environment. Task globals are not merged into the receiver.
Dynamic `eval` and direct or indirect namespace builtin calls use the executing
function's namespace too. They do not expose lexical frame slots. See
[Runtime semantics and validation](doc/runtime.md) for cleanup, diagnostics,
copy budgets and verification commands.

### Task scopes

Use an explicit scope when work must finish before execution leaves a block:

```lisp
(task-scope
  (: a (par (work 20)))
  (: b (par (work 22)))
  (+ (wait a) (wait b)))

(task-scope :limit 32
  (par (work 20))
  42)                         ; waits for work, then returns 42
```

A scope creates a local function scope and returns its last expression's value
(`()` for an empty body). Put local definitions at the beginning of the body,
or use `let`. It joins all tasks it creates and their descendants, including
unwaited tasks. Completed task handles can be returned and waited on later.
Tasks created outside the scope do not become its members merely by waiting on
or copying their handles.

Any member failure cancels the group immediately. Scope exit joins the group
and propagates the error; catching a `wait` error inside the scope does not
make the group successful again. Catch around `task-scope` for recovery.
A body failure also cancels and joins members. A body error takes precedence,
except that cancellation caused by a failed task reports the original task
error. Concurrent failures report the first recorded task error.

The default limit is 1024 active member tasks, including descendants that
inherit the same scope. An explicit nested scope establishes a new budget.
`:limit` accepts a positive integer expression evaluated once on entry.
Exceeding the limit raises an
error before copying the new task's environment; it does not wait for a free
slot. This prevents a worker pool deadlock when tasks wait for descendants.
Nested explicit scopes have their own limits and inherit cancellation. The
limit bounds task count per scope, not bytes or total process concurrency.
An optional `:copy-bytes` budget bounds cumulative managed bytes copied for
snapshots and completion graphs; zero means unlimited. It excludes worker
execution allocations, Go metadata, and `wait` result imports.
Outside a scope, existing `par` behavior is unchanged and has no task limit.

Cancellation is cooperative: scope cleanup waits for members to stop. A host
callback or blocking I/O operation that ignores cancellation can delay scope
exit indefinitely. A scope does not close externally shared resources.

Tasks inherit the context of the invocation that creates them. Cancelling that
context stops VM execution cooperatively. A cancelled waiting invocation can
stop waiting; blocking host operations must themselves support cancellation.
Outside a scope, unwaited tasks run until completion or context cancellation.
The CLI does not
implicitly join tasks on exit: explicitly `wait` for work that must finish.

Go functions and registered Go objects remain shared host capabilities. Host
callbacks must use the `*Interp` passed to them and synchronize any shared Go
state; they must not capture and operate on the parent interpreter or its Lisp
values. Task isolation does not isolate file, network, or other external side
effects.

## Concurrent interpreters

Each `Interp` owns its Lisp bindings, macro definitions, builtins, heap, VM
stack, and standard streams. Separate interpreters can be created and evaluated
concurrently; interned symbol names and IDs are the only shared runtime data.
An individual `Interp` is single-owner while an evaluation or host call is
active. Synchronize access if multiple goroutines use the same interpreter.
Use `EvalStringContext` or `CallContext` when host code needs context checks;
context cancellation is checked before and during VM execution.

## Plugin host

For applications that embed Lisp as a plugin language, import the optional
`golisp/plugin` package and use `PluginHost` rather than scattering direct
registrations through the application:

```go
import "golisp/plugin"

type MetricsPlugin struct{}

func (MetricsPlugin) Name() string { return "metrics" }

func (MetricsPlugin) Setup(h *plugin.PluginHost) error {
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

g := interp.NewInterp()
h, err := plugin.Load(g, MetricsPlugin{})
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
Use a separate `interp.Interp` when state isolation is required. The default
interpreter includes filesystem and networking primitives. Only run trusted
plugins; separate interpreter state does not restrict operating-system access.

Heap-backed `Value` objects belong to their interpreter. Keep the interpreter
alive while accessing them from Go (`runtime.KeepAlive` after the last access
when needed), and retain long-lived callbacks in an interpreter binding so its
collector can trace them. Do not pass these values to a different interpreter.

The runtime uses NaN-boxed pointer arithmetic. Race detection currently requires
`go test -race -gcflags=all=-d=checkptr=0 ./...`; default checkptr instrumentation
rejects the pointer representation. This does not establish memory safety for
untrusted programs.

## Go interop

Register anything with one call. When the name is empty it is derived from Go,
so `Add` becomes `Add` and `*Person` becomes `Person`.

```go
import "golisp/interp"

type Person struct {
	Name string
	Age  int
	Tags map[string]string
}

func (p *Person) Greet(prefix string) string { return prefix + ", " + p.Name }
func (p *Person) Birthday() int              { p.Age++; return p.Age }
func Add(a, b int) int                        { return a + b }

g := interp.NewInterp()
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

The `perf.lsp` suite from the femtolisp project is the reference workload, but there is
no checked-in Go benchmark harness yet, so no speedup claim is made here.
Measured on an Apple M4 against `femtolisp` for three shared workloads, this
implementation is roughly 2.3x to 3.1x slower.
