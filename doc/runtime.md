# Runtime semantics and validation

## Names and evaluation

Functions use lexical bindings. `let` creates locals; `begin` sequences forms.
Leading internal definitions belong to the function frame. Module functions
resolve module bindings before ambient globals. Returned task closures retain
the task's copied namespace, including absent bindings and module fallbacks.

Dynamic `eval`, `bound?`, `top-level-value` and `set-top-level-value!` use the
executing function's namespace. Direct and indirect builtin calls agree.
A module currently being loaded supplies the compilation namespace for its
forms. Dynamic evaluation does not expose lexical frame slots: use closure
arguments/captures to access locals. Environment state is restored on return,
tail calls and error unwinding. Host calls outside Lisp execution use ambient
bindings.

## Guaranteed cleanup

```lisp
(unwind-protect
  (use-resource resource)
  (close resource))
```

The first expression is protected. Remaining forms are cleanup, in order.
Cleanup runs once on normal return, errors, panic, or cooperative cancellation.
Normal return preserves the protected result. A cleanup failure takes precedence
if both computations fail; otherwise the original error is preserved.

Cleanup inherits context values but removes cancellation and deadlines. There
is no automatic cleanup timeout: blocking cleanup can delay completion. The
mechanism cannot recover from process termination, runtime fatal errors or
`runtime.Goexit` in a host callback. It does not forcibly interrupt Go code.
`with-file` and `call-with-stream` now use this mechanism.

## Copy budgets and scheduling

```lisp
(task-scope :limit 32 :copy-bytes 1048576
  (let ((task (par (work))))
    (wait task)))
```

`:limit` bounds simultaneous member tasks; `:copy-bytes` bounds cumulative
managed object bytes copied for task snapshots and completion result graphs.
Options must precede the body. Duplicate options are errors. The copy budget
is a nonnegative integer; zero (default) means unlimited. Each unique object
is charged before its destination allocation, including headers and string
padding. Aliases within one copy are charged once. Charges are not refunded.
Inherited descendants share the budget; explicit nested scopes use independent
budgets. Exhaustion cancels the group and raises an error.

This budget excludes Go metadata/maps, arena slack, allocations made while
executing task code, and result imports performed by `wait`. It is not a process
memory quota. Outside task scopes there is no automatic task budget. No bounded
worker pool is introduced: tasks remain goroutines, avoiding pool starvation
when tasks wait on descendants. Long list spines copy iteratively while
preserving cycles, aliases and dotted tails. Environment snapshots still copy
reachable globals; capture-only snapshots and full resource quotas are not
implemented.

## Error diagnostics

Go callers can use `interp.FormatError(err)` for a diagnostic string and
`interp.ErrorTrace(err)` for a copy of the structured frames. The CLI formats
errors this way. `err.Error()` and Lisp error condition values retain their
existing message format. Attaching context clones errors and trace slices, so
concurrent waiters cannot modify a shared task error.

Locations use one-based lines and Unicode character columns. A function's
location identifies its enclosing top-level source form, not the exact failing
subexpression. Generated macro code inherits that form. Runtime traces contain
surviving Lisp frames; tail-call history is intentionally not accumulated.
Dynamic forms constructed with `eval` have no invented source coordinates.
Context and arbitrary host errors retain their original identity and may lack
Lisp source traces. Files, imported modules and embedded libraries keep their
own source names.

## Reproducible checks

```sh
go test ./... -count=1
go test -race -gcflags=all=-d=checkptr=0 ./... -count=1
go build ./...
go test ./interp -run '^$' -fuzz '^FuzzReadAll' -fuzztime=3s -parallel=2
go test ./interp -run '^$' -fuzz '^FuzzReadStringRoundTrip$' -fuzztime=3s -parallel=2
```

Inspect `read_fuzz_test.go` for exact fuzz target names. Parser fuzz inputs are
bounded and exclude read-time evaluation (`#.`); this is not an untrusted-code
sandbox. Stress tests combine task completion, errors, modules and forced GC.
The NaN-boxed pointer representation requires disabling checkptr for race runs;
passing race tests is not proof of pointer or memory safety.
