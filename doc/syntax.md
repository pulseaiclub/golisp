# golisp Syntax Reference

golisp is a bytecode-compiling Lisp interpreter; the dialect is based on
femtolisp. This document describes the language as implemented: literals,
special forms, builtins, the standard library, and Go interop.

- Truth semantics: only `#f` is false. Everything else — including the empty
  list `()` and `0` — is true.
- Identifiers are case-sensitive; `-`, `!`, `?`, `>`, `*`, `.` may all appear
  in names.
- Evaluating an unbound variable raises `unbound-error`.

## Contents

1. [Lexical syntax and literals](#lexical-syntax-and-literals)
2. [Special forms](#special-forms)
3. [Builtins](#builtins)
4. [Standard library (defined in Lisp)](#standard-library-defined-in-lisp)
5. [Macros](#macros)
6. [Errors and exceptions](#errors-and-exceptions)
7. [Go interop](#go-interop)

---

## Lexical syntax and literals

### Comments

```lisp
; line comment to end of line
#| block comment
   #| nestable |#
   still a comment |#
#; (datum comment)  ; discards the next complete s-expression
```

### Numbers

| Syntax | Result |
|---|---|
| `42` `-7` | fixnum (within ±2^47-1) |
| `0xff` `0x10` | hexadecimal fixnum |
| `3.14` `.5` `1e3` | double-precision float |
| out-of-range integer | silently becomes a float |

### Characters and strings

```lisp
#\a #\space #\newline #\tab      ; character literals

"hello\n"                        ; string
```

String escapes: `\n` `\t` `\r` `\0` `\\` `\"` `\xHH` (hex code point).
Any other escaped character stands for itself.

### Booleans and the empty list

```lisp
#t #f     ; booleans
()        ; empty list (nil) — note: () is truthy
```

### Lists and dotted pairs

```lisp
(1 2 3)
(1 . 2)                    ; dotted pair
(1 2 . 3)                  ; improper list
[1 2 3]                    ; brackets are equivalent to parentheses
```

### Vectors

```lisp
#(1 2 3)                   ; vector literal
```

### Quoting

```lisp
'x                         ; = (quote x)
`(1 ,b ,@lst)              ; quasiquote / unquote / unquote-splicing
|symbol with spaces|       ; symbol literal wrapped in vertical bars
```

### Read-time evaluation

```lisp
#.(+ 1 2)                  ; evaluated by the reader, reads as 3
```

---

## Special forms

### Definitions and assignment

```lisp
(define x 10)                    ; define a global variable
(define (f a b) body...)         ; define a function
(define (f a . rest) body...)    ; fixed params + rest collected into a list
(define (f . args) body...)      ; all arguments collected into a list

(set! x 20)                      ; mutate an existing binding (also Go fields,
                                 ; see interop)
```

### lambda and application

```lisp
(lambda (x y) (+ x y))
(lambda args ...)                ; variadic
(lambda (x . rest) ...)

(f 1 2)                          ; call
(apply f 1 2 '(3 4))             ; = (f 1 2 3 4)
```

### Branching and logic

```lisp
(if test then)                   ; () when test is #f
(if test then else)

(and a b)                        ; short-circuit; returns the last value evaluated
(or a b)                         ; #f when all are false

(cond (test1 body...)
      (test2)                    ; yields test2's value when true
      (test3 => proc)            ; calls (proc test3) when true
      (else body...))            ; fallback clause

(when test body...)              ; macro: body when test is true
(unless test body...)            ; macro: body when test is false
```

### Sequencing and binding

```lisp
(begin e1 e2 ...)                ; sequence, returns the last value

(let ((a 1) (b 2)) body...)      ; parallel binding (initializers evaluated
                                 ; in the outer scope)
(let* ((a 1) (b (+ a 1))) ...)   ; sequential binding
(let loop ((i 0)) body...)       ; named let (note: the name is not bound
                                 ; inside the body, so it cannot recurse yet)

(label f (lambda (x) ...))       ; self-referencing binding; f may recurse
```

### Loops

```lisp
(while test body...)             ; returns () when test becomes #f
(dotimes (i n) body...)          ; i from 0 to n-1
(for-each f lst)                 ; standard library: applies f to each element

; for recursion, use define or label:
(define (fib n) (if (< n 2) n (+ (fib (- n 1)) (fib (- n 2)))))
```

### Quoting and macro definition

```lisp
(quote x)
(quasiquote template)            ; ,x interpolates, ,@lst splices a list

(define-macro (name args) body...)   ; macro receives source-form arguments,
                                     ; returns code
(define-macro (when test . body)
  (list 'if test (cons 'begin body) ()))
```

### Miscellaneous

```lisp
(prog1 e1 e2 ...)                ; evaluates in order, returns e1's value

(trycatch (lambda () body)       ; catches Lisp errors; body must be a
          (lambda (err) ...))    ; zero-argument thunk, err binds the error
(time expr)                      ; macro: prints elapsed time, returns expr's value
(eval form)                      ; evaluates a form at top level
```

---

## Builtins

The following are compiled to bytecode instructions or native Go functions
and cannot be redefined (`constant?` is `#t` for them).

### Arithmetic

| Function | Args | Notes |
|---|---|---|
| `+ - * /` | variadic / min 2 | numeric ops, fixnum and float |
| `div0` | 2 | truncating division, raises `divide-error` on 0 |
| `mod` | 2 | remainder, raises `divide-error` on 0 |
| `abs` `truncate` | 1 | unary ops |
| `=` `<` | 2 | numeric comparison |
| `compare` | 2 | total order across types, returns -1/0/1 |
| `logand` `ash` | 2 | bitwise and, arithmetic shift |

### Type predicates

| Function | Notes |
|---|---|
| `eq?` `eqv?` `equal?` | three equality levels: identity / identity plus numbers / deep |
| `atom?` `pair?` `cons?` `null?` | structure tests (`atom?` is not-a-cons) |
| `not` | logical negation |
| `number?` `fixnum?` `integer?` | numeric tests |
| `boolean?` `symbol?` `string?` `char?` | scalar tests |
| `vector?` `function?` `procedure?` `builtin?` | composite type tests |
| `bound?` | is the variable bound |
| `list?` | `()` or a proper list |
| `eof-object?` | is the end-of-file object |
| `constant?` | is the binding a non-redefinable builtin |

### Lists

| Function | Args | Notes |
|---|---|---|
| `cons` | 2 | build a pair |
| `list` | variadic | build a list |
| `car` `cdr` | 1 | head / tail (legal on `()`, returns `()`) |
| `set-car!` `set-cdr!` | 2 | mutate in place |
| `length` | 1 | list / vector / string / table length |

### Vectors

| Function | Args | Notes |
|---|---|---|
| `vector` | variadic | build a vector |
| `vector.alloc` | 1 | allocate n slots of `()` |
| `aref` | 2 | vector / string indexing (raises `bounds-error`) |
| `aset!` | 3 | vector store |

### Tables (hash tables)

| Function | Args | Notes |
|---|---|---|
| `table` | variadic | `(table k1 v1 k2 v2 ...)` |
| `get` | 2-3 | `get` returns the third argument when the key is missing (default: error) |
| `put!` `del!` | | insert / delete |
| `has?` | 2 | key present |

### Strings and symbols

| Function | Args | Notes |
|---|---|---|
| `string` | variadic | concatenates the display form of each argument |
| `string.length` | 1 | byte length |
| `string.sub` | 3 | `(string.sub s start end)` |
| `string.find` | 2-3 | substring search, index or `#f` |
| `string.char` | 2 | character at byte index |
| `string.inc` `string.dec` | 3 | move n UTF-8 character boundaries forward / back |
| `symbol` | 1 | string to symbol |
| `gensym` | 0 | fresh unique symbol |
| `number->string` | 1-2 | number to string |
| `string->symbol` | — | use `(symbol s)` |

### I/O and streams

| Function | Args | Notes |
|---|---|---|
| `princ` | variadic | display output (no quotes) to stdout |
| `print` | variadic | write-style output (strings quoted) |
| `newline` | 0 | output a newline |
| `write` `write-line` | 1+ | write to a stream (first argument), `write-line` appends a newline |
| `io.write` | variadic | with a stream argument equals `write`, otherwise stdout |
| `read-line` | 1 | read one line; eof-object at end of stream |
| `read-all` `read-n` | | read the whole stream / read n bytes |
| `flush` `close` | 1 | flush / close a stream or listener |
| `stream.name` | 1 | stream name |

### Files

| Function | Notes |
|---|---|
| `(open path [mode])` | open a file stream; mode is `:read` `:write` `:append` `:read-write` `:new`, default `:read` |
| `read-file` / `write-file` / `append-file` | whole-file I/O |
| `file-exists?` / `delete-file` | existence / deletion |

### Networking

| Function | Args | Notes |
|---|---|---|
| `tcp-connect` | 2 | `(tcp-connect host port)`, returns a stream |
| `tcp-listen` | 2 | listen; port 0 binds an ephemeral port |
| `tcp-accept` | 1 | accept a connection, returns a stream |
| `listener-port` `listener-addr` | 1 | listener info |

Once connected, `read-line`, `write-line`, and `close` behave exactly like
file streams.

### Miscellaneous

| Function | Notes |
|---|---|
| `(error msg...)` | raise a Lisp error (arguments joined in display form) |
| `(raise v)` | raise using v's display form |
| `time` `time.now` | current time in seconds (float) |
| `rand` | simple pseudo-random number |
| `load` | load and evaluate a Lisp file |
| `exit` | exit the process, optional status code |
| `void` | returns the unspecified value |
| `eof-object` | returns the eof object |
| `top-level-value` / `set-top-level-value!` | read / write a global binding by symbol |

---

## Standard library (defined in Lisp)

Every interpreter loads the following Lisp functions at startup (source in
`interp/stdlib.lsp`); user code may redefine them:

```lisp
; comparison and arithmetic extras
> <= >= 1+ 1- zero? negative? positive? even? odd? mod0 identity

; list lookup and access
member memv assoc assv
caar cadr cdar cddr caadr caddr cdadr cdddr

; list operations
append2 append reverse reverse! reverse-into
list-head list-tail filter map for-each
foldl foldr any every length= length>
list->vector map-int

; I/O wrappers
read-lines file->string string->file with-file call-with-stream
net.readLine net.writeLine
```

---

## Macros

Loaded at startup (source in `interp/macros.lsp`):

| Macro | Expands to |
|---|---|
| `(when test body...)` | `(if test (begin body...) ())` |
| `(unless test body...)` | `(if (not test) (begin body...) ())` |
| `(time expr)` | prints "Elapsed time: N seconds", returns expr's value |

---

## Errors and exceptions

Lisp errors are objects with a category. Catch them with `trycatch`, whose
first argument is a zero-argument thunk (invoked by the VM) and whose second
argument receives the error object:

```lisp
(trycatch (lambda () (/ 1 0))
          (lambda (err) (princ "caught: " err "\n") 'handled))
; => handled
```

Error categories (symbols): `type-error` `arg-error` `unbound-error`
`bounds-error` `divide-error` `io-error` `parse-error` `error`.

Errors returned by host Go functions are raised as Lisp errors and can be
caught the same way.

Stream and file operations never crash the process: missing files, closing
twice, writing a read-only handle, and connection refused all raise catchable
Lisp errors.

---

## Go interop

After the host registers Go values with `interp.Interp.Def` / `Import`, Lisp
accesses them with plain dotted syntax — there are no special forms:

```lisp
(Add 20 22)              ; call a registered Go function
p.Name                   ; read a field / map entry
p.Tags.kind              ; nested access
(set! p.Age 40)          ; write a field
(p.Greet "hi")           ; call a method
p.Birthday               ; zero-arg method read as a value
(p.Birthday)             ; or called
(str.ToUpper "quiet")    ; function under an Import prefix
```

Rules:

- The first component of a dotted name must be a registered Go value;
  otherwise the name resolves as an ordinary global, so library names such as
  `string.length` are unaffected.
- Conversion is automatic: Go numbers/strings/booleans ↔ Lisp numbers/
  strings/booleans, slices/arrays ↔ vectors, maps ↔ tables, multiple return
  values ↔ lists, and a trailing `error` return is raised as a Lisp error.
- structs, pointers, funcs, and channels are wrapped as boxed Go values.
- Only explicitly registered names are visible; Lisp cannot import arbitrary
  Go packages.

The `go.call`, `go.get`, `go.set!`, and `go.apply` builtins exist for explicit
use, but dotted syntax is normally shorter.
