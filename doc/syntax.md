# golisp 语法参考

golisp 是一个字节码编译的 Lisp 解释器，方言基于 femtolisp。本文按实际实现
描述语言的全部语法：字面量、特殊形式、内建函数、标准库和 Go 互操作。

- 真值语义：只有 `#f` 为假，其余一切值（包括空列表 `()` 和 `0`）都为真。
- 标识符区分大小写；`-`、`!`、`?`、`>`、`*`、`.` 都可以出现在名字里。
- 未绑定的变量在求值时报 `unbound-error`。

## 目录

1. [词法与字面量](#词法与字面量)
2. [特殊形式](#特殊形式)
3. [内建函数](#内建函数)
4. [标准库（Lisp 定义）](#标准库lisp-定义)
5. [宏](#宏)
6. [错误与异常](#错误与异常)
7. [Go 互操作](#go-互操作)

---

## 词法与字面量

### 注释

```lisp
; 行注释到行尾
#| 块注释
   #| 可以嵌套 |#
   还在注释里 |#
#; (表达式注释)  ; 丢弃下一个完整的 s 表达式
```

### 数字

| 写法 | 结果 |
|---|---|
| `42` `-7` | fixnum（±2^47-1 范围内） |
| `0xff` `0x10` | 十六进制 fixnum |
| `3.14` `.5` `1e3` | 双精度浮点 |
| 超范围的大整数 | 自动转为浮点 |

### 字符与字符串

```lisp
#\a #\space #\newline #\tab      ; 字符字面量

"hello\n"                        ; 字符串
```

字符串转义：`\n` `\t` `\r` `\0` `\\` `\"` `\xHH`（十六进制码点）。
其他转义序列按原字符处理。

### 布尔与空列表

```lisp
#t #f     ; 布尔值
()        ; 空列表（nil），注意：() 为真
```

### 列表与点对

```lisp
(1 2 3)
(1 . 2)                    ; 点对
(1 2 . 3)                  ; 不完整列表
[1 2 3]                    ; 方括号与圆括号等价
```

### 向量

```lisp
#(1 2 3)                   ; 向量字面量，字面量本身不可变地存储在常量池
```

### 引用

```lisp
'x                         ; = (quote x)
`(1 ,b ,@lst)              ; quasiquote / unquote / unquote-splicing
|symbol with spaces|       ; 竖线包裹的符号字面量
```

### 读取期求值

```lisp
#.(+ 1 2)                  ; 读取时求值，读入为 3
```

---

## 特殊形式

### 定义与赋值

```lisp
(define x 10)                    ; 定义全局变量
(define (f a b) body...)         ; 定义函数
(define (f a . rest) body...)    ; 定长参数 + 剩余参数收集为列表
(define (f . args) body...)      ; 全部参数收集为列表

(set! x 20)                      ; 修改既有绑定（也支持 Go 值的字段，见互操作）
```

### lambda 与函数调用

```lisp
(lambda (x y) (+ x y))
(lambda args ...)                ; 不定参数
(lambda (x . rest) ...)

(f 1 2)                          ; 调用
(apply f 1 2 '(3 4))             ; 等价于 (f 1 2 3 4)
```

### 分支与逻辑

```lisp
(if test then)                   ; test 为假（#f）时返回 ()
(if test then else)

(and a b)                        ; 短路求值，返回最后求值的表达式
(or a b)                         ; 全为假时返回 #f

(cond (test1 body...)
      (test2)                    ; 真时返回 test2 的值
      (test3 => proc)            ; 真时调用 (proc test3)
      (else body...))            ; 兜底分支

(when test body...)              ; 宏：test 为真时执行 body
(unless test body...)            ; 宏：test 为假时执行 body
```

### 顺序与作用域

```lisp
(begin e1 e2 ...)                ; 顺序求值，返回最后一个值

(let ((a 1) (b 2)) body...)      ; 并行绑定（初值在外层作用域求值）
(let* ((a 1) (b (+ a 1))) ...)   ; 顺序绑定
(let loop ((i 0)) body...)       ; 命名 let（注意：名字未绑定到体内，暂不能递归调用）

(label f (lambda (x) ...))       ; 绑定自引用的函数，f 可递归调用
```

### 循环

```lisp
(while test body...)             ; test 为假时返回 ()
(dotimes (i n) body...)          ; i 从 0 到 n-1
(for-each f lst)                 ; 标准库：逐元素调用 f

; 递归一般用 define 或 label：
(define (fib n) (if (< n 2) n (+ (fib (- n 1)) (fib (- n 2)))))
```

### 引号与宏定义

```lisp
(quote x)
(quasiquote template)            ; 模板中 ,x 插值、,@lst 拼接列表

(define-macro (name args) body...)   ; 宏收到源码形式的参数，返回代码
(define-macro (when test . body)
  (list 'if test (cons 'begin body) ()))
```

### 杂项

```lisp
(prog1 e1 e2 ...)                ; 依次求值，返回 e1 的值

(trycatch (lambda () body)       ; 捕获 Lisp 错误；body 必须是零参 thunk，
          (lambda (err) ...))    ; err 绑定错误对象
(time expr)                      ; 宏：打印耗时并返回 expr 的值
(eval form)                      ; 在顶层求值一个形式
```

---

## 内建函数

以下函数编译为字节码指令或原生 Go 函数，不可重定义（`constant?` 为 `#t`）。

### 算术

| 函数 | 参数 | 说明 |
|---|---|---|
| `+ - * /` | 不定 / 至少 2 个 | 数值运算，自动区分 fixnum 与浮点 |
| `div0` | 2 | 向零截断的除法，÷0 报 `divide-error` |
| `mod` | 2 | 取模，÷0 报 `divide-error` |
| `abs` `truncate` | 1 | 一元运算 |
| `=` `<` | 2 | 数值比较 |
| `compare` | 2 | 全类型比较，返回 -1/0/1 |
| `logand` `ash` | 2 | 位运算、算术移位 |

### 类型谓词

| 函数 | 说明 |
|---|---|
| `eq?` `eqv?` `equal?` | 三层相等：引用 / 含数值的引用 / 深度递归 |
| `atom?` `pair?` `cons?` `null?` | 结构判断（`atom?` 为非 cons） |
| `not` | 逻辑非 |
| `number?` `fixnum?` `integer?` | 数值判断 |
| `boolean?` `symbol?` `string?` `char?` | 标量判断 |
| `vector?` `function?` `procedure?` `builtin?` | 复合类型判断 |
| `bound?` | 变量是否已绑定 |
| `list?` | 是 `()` 或以 `()` 结尾的真列表 |
| `eof-object?` | 是否为文件结束对象 |
| `constant?` | 是否为不可重定义的内建绑定 |

### 列表

| 函数 | 参数 | 说明 |
|---|---|---|
| `cons` | 2 | 构造点对 |
| `list` | 不定 | 构造列表 |
| `car` `cdr` | 1 | 取头 / 尾（`()` 合法，返回 `()`） |
| `set-car!` `set-cdr!` | 2 | 原地修改 |
| `length` | 1 | 列表 / 向量 / 字符串 / 表的长度 |

### 向量

| 函数 | 参数 | 说明 |
|---|---|---|
| `vector` | 不定 | 构造向量 |
| `vector.alloc` | 1 | 分配 n 个 `()` 的向量 |
| `aref` | 2 | 向量 / 字符串索引（越界报 `bounds-error`） |
| `aset!` | 3 | 向量写入 |

### 表（哈希表）

| 函数 | 参数 | 说明 |
|---|---|---|
| `table` | 不定 | `(table k1 v1 k2 v2 ...)` 构造 |
| `get` | 2-3 | `get` 缺键时返回第三参数（默认报错） |
| `put!` `del!` | | 写入 / 删除 |
| `has?` | 2 | 键是否存在 |

### 字符串与符号

| 函数 | 参数 | 说明 |
|---|---|---|
| `string` | 不定 | 各值的显示形式拼接为字符串 |
| `string.length` | 1 | 字节长度 |
| `string.sub` | 3 | 子串 `(string.sub s start end)` |
| `string.find` | 2-3 | 子串查找，返回下标或 `#f` |
| `string.char` | 2 | 按字节下标取字符 |
| `string.inc` `string.dec` | 3 | UTF-8 字符边界前进 / 后退 n |
| `symbol` | 1 | 字符串转符号 |
| `gensym` | 0 | 生成不重复符号 |
| `number->string` | 1-2 | 数值转字符串 |
| `string->symbol` | — | 用 `(symbol s)` |

### I/O 与流

| 函数 | 参数 | 说明 |
|---|---|---|
| `princ` | 不定 | 显示输出（不带引号）到 stdout |
| `print` | 不定 | write 形式输出（字符串带引号） |
| `newline` | 0 | 输出换行 |
| `write` `write-line` | 1+ | 写入流（首参数为流），`write-line` 追加换行 |
| `io.write` | 不定 | 有流参数时等同 `write`，否则写 stdout |
| `read-line` | 1 | 读一行，流结束返回 eof-object |
| `read-all` `read-n` | | 读整个流 / 读 n 字节 |
| `flush` `close` | 1 | 刷新 / 关闭流或监听器 |
| `stream.name` | 1 | 流的名字 |

### 文件

| 函数 | 说明 |
|---|---|
| `(open path [mode])` | 打开文件流，mode 为符号 `:read` `:write` `:append` `:read-write` `:new`，缺省 `:read` |
| `read-file` / `write-file` / `append-file` | 整文件读写 |
| `file-exists?` / `delete-file` | 存在性 / 删除 |

### 网络

| 函数 | 参数 | 说明 |
|---|---|---|
| `tcp-connect` | 2 | 连接 `(tcp-connect host port)`，返回流 |
| `tcp-listen` | 2 | 监听，port 为 0 时绑定临时端口 |
| `tcp-accept` | 1 | 接受连接，返回流 |
| `listener-port` `listener-addr` | 1 | 查询监听器信息 |

连接一旦建立，`read-line`、`write-line`、`close` 与文件流完全一致。

### 其他

| 函数 | 说明 |
|---|---|
| `(error msg...)` | 抛出 Lisp 错误（各参数按显示形式拼接） |
| `(raise v)` | 以 v 的显示形式抛错 |
| `time` `time.now` | 当前时间的秒数（浮点） |
| `rand` | 简单伪随机数 |
| `load` | 加载并求值一个 Lisp 文件 |
| `exit` | 退出进程，可带退出码 |
| `void` | 返回 unspecified 值 |
| `eof-object` | 返回 eof 对象 |
| `top-level-value` / `set-top-level-value!` | 按符号读写全局绑定 |

---

## 标准库（Lisp 定义）

每个解释器启动时自动加载以下 Lisp 函数（源码在 `interp/stdlib.lsp`），
可被用户代码覆盖：

```lisp
; 比较与算术补充
> <= >= 1+ 1- zero? negative? positive? even? odd? mod0 identity

; 列表查找与访问
member memv assoc assv
caar cadr cdar cddr caadr caddr cdadr cdddr

; 列表操作
append2 append reverse reverse! reverse-into
list-head list-tail filter map for-each
foldl foldr any every length= length>
list->vector map-int

; I/O 封装
read-lines file->string string->file with-file call-with-stream
net.readLine net.writeLine
```

---

## 宏

启动时自动加载（源码在 `interp/macros.lsp`）：

| 宏 | 展开为 |
|---|---|
| `(when test body...)` | `(if test (begin body...) ())` |
| `(unless test body...)` | `(if (not test) (begin body...) ())` |
| `(time expr)` | 打印 "Elapsed time: N seconds" 并返回 expr 的值 |

---

## 错误与异常

Lisp 错误是带类别的对象，用 `trycatch` 捕获。`trycatch` 的第一个参数是
零参数 thunk（由 VM 调用），第二个参数收到错误对象：

```lisp
(trycatch (lambda () (/ 1 0))
          (lambda (err) (princ "caught: " err "\n") 'handled))
; => handled
```

错误类别（符号）：`type-error` `arg-error` `unbound-error`
`bounds-error` `divide-error` `io-error` `parse-error` `error`。

宿主 Go 函数返回的 `error` 也会作为 Lisp 错误抛出，可以被捕获。

流和文件操作不会崩溃进程：文件不存在、重复关闭、写只读句柄、
连接拒绝等都抛出可捕获的 Lisp 错误。

---

## Go 互操作

宿主程序用 `interp.Interp.Def` / `Import` 注册 Go 值后，Lisp 用普通点号
语法访问，没有特殊形式：

```lisp
(Add 20 22)              ; 调用注册的 Go 函数
p.Name                   ; 读字段 / map 项
p.Tags.kind              ; 嵌套访问
(set! p.Age 40)          ; 写字段
(p.Greet "hi")           ; 调用方法
p.Birthday               ; 无参方法读为值
(p.Birthday)             ; 也可以调用
(str.ToUpper "quiet")    ; Import 前缀下的函数
```

规则：

- 点号名字的第一个分量必须是已注册的 Go 值，否则按普通全局变量处理，
  因此 `string.length` 这类库名不受影响。
- 类型转换自动进行：Go 数字/字符串/布尔 ↔ Lisp 数值/字符串/布尔，
  slice/数组 ↔ 向量，map ↔ 表，多返回值 ↔ 列表，末尾的 `error`
  返回值抛出为 Lisp 错误。
- struct、指针、func、channel 包装为 boxed Go 值。
- 只能看到显式注册的名字，Lisp 无法导入任意 Go 包。

也可以显式使用内建 `go.call`、`go.get`、`go.set!`、`go.apply`，
但通常点号语法更简洁。
