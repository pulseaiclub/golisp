package golisp

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Reader: s-expression parser for femtolisp syntax.

type reader struct {
	src string
	pos int
	g   *Interp
}

func (g *Interp) ReadAll(src string) ([]Value, error) {
	r := &reader{src: src, g: g}
	var forms []Value
	for {
		r.skipWS()
		if r.pos >= len(r.src) {
			return forms, nil
		}
		v, err := r.read()
		if err != nil {
			return forms, err
		}
		forms = append(forms, v)
	}
}

func (g *Interp) Read(src string) (Value, error) {
	r := &reader{src: src, g: g}
	r.skipWS()
	return r.read()
}

func (r *reader) skipWS() {
	for r.pos < len(r.src) {
		c := r.src[r.pos]
		if c == ';' {
			for r.pos < len(r.src) && r.src[r.pos] != '\n' {
				r.pos++
			}
			continue
		}
		if c == '#' && r.pos+1 < len(r.src) && r.src[r.pos+1] == '|' {
			// block comment #| ... |# (nestable)
			depth := 1
			r.pos += 2
			for r.pos < len(r.src) && depth > 0 {
				if r.pos+1 < len(r.src) && r.src[r.pos] == '#' && r.src[r.pos+1] == '|' {
					depth++
					r.pos += 2
				} else if r.pos+1 < len(r.src) && r.src[r.pos] == '|' && r.src[r.pos+1] == '#' {
					depth--
					r.pos += 2
				} else {
					r.pos++
				}
			}
			continue
		}
		if c == '#' && r.pos+1 < len(r.src) && r.src[r.pos+1] == ';' {
			// datum comment
			r.pos += 2
			r.skipWS()
			r.read() // discard
			continue
		}
		if c <= ' ' {
			r.pos++
			continue
		}
		break
	}
}

func (r *reader) peek() byte {
	if r.pos < len(r.src) {
		return r.src[r.pos]
	}
	return 0
}

func isSymChar(c byte) bool {
	switch c {
	case 0, ' ', '\t', '\n', '\r', '(', ')', '[', ']', '{', '}', '"', '\'', '`', ',', ';':
		return false
	}
	return true
}

func (r *reader) read() (Value, error) {
	r.skipWS()
	if r.pos >= len(r.src) {
		return EOFVal, &LispError{kind: symParseError, msg: "unexpected end of input"}
	}
	c := r.src[r.pos]
	switch {
	case c == '(' || c == '[':
		return r.readList(c)
	case c == ')' || c == ']':
		return 0, &LispError{kind: symParseError, msg: "unexpected )"}
	case c == '\'':
		r.pos++
		v, err := r.read()
		if err != nil {
			return 0, err
		}
		return r.g.list2(SymbolValue(symQuote), v), nil
	case c == '`':
		r.pos++
		v, err := r.read()
		if err != nil {
			return 0, err
		}
		return r.g.list2(SymbolValue(symQuasiQuote), v), nil
	case c == ',':
		r.pos++
		sym := symUnquote
		if r.peek() == '@' {
			r.pos++
			sym = symUnquoteS
		}
		v, err := r.read()
		if err != nil {
			return 0, err
		}
		return r.g.list2(SymbolValue(sym), v), nil
	case c == '"':
		return r.readString()
	case c == '#':
		return r.readSharp()
	case c == '.':
		// could be a number like .5 or a dot
		if r.pos+1 < len(r.src) && r.src[r.pos+1] >= '0' && r.src[r.pos+1] <= '9' {
			return r.readAtom()
		}
		return r.readAtom()
	default:
		return r.readAtom()
	}
}

func (r *reader) readList(open byte) (Value, error) {
	close := byte(')')
	if open == '[' {
		close = ']'
	}
	r.pos++
	var head, tail Value
	head = Nil
	r.skipWS()
	if r.peek() == close {
		r.pos++
		return Nil, nil
	}
	for {
		r.skipWS()
		if r.pos >= len(r.src) {
			return 0, &LispError{kind: symParseError, msg: "unterminated list"}
		}
		c := r.src[r.pos]
		if c == close || (c == ')' || c == ']') {
			r.pos++
			return head, nil
		}
		// dotted pair?
		if c == '.' && r.pos+1 < len(r.src) && !isSymChar(r.src[r.pos+1]) {
			r.pos++
			d, err := r.read()
			if err != nil {
				return 0, err
			}
			r.skipWS()
			if r.peek() == ')' || r.peek() == ']' {
				r.pos++
			}
			if head == Nil {
				return d, nil
			}
			SetCdr(tail, d)
			return head, nil
		}
		v, err := r.read()
		if err != nil {
			return 0, err
		}
		cell := r.g.Cons(v, Nil)
		if head == Nil {
			head = cell
		} else {
			SetCdr(tail, cell)
		}
		tail = cell
	}
}

func (r *reader) readString() (Value, error) {
	r.pos++ // skip "
	var sb strings.Builder
	for r.pos < len(r.src) {
		c := r.src[r.pos]
		if c == '"' {
			r.pos++
			return r.g.NewString(sb.String()), nil
		}
		if c == '\\' && r.pos+1 < len(r.src) {
			r.pos++
			switch r.src[r.pos] {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case 'r':
				sb.WriteByte('\r')
			case '0':
				sb.WriteByte(0)
			case '\\':
				sb.WriteByte('\\')
			case '"':
				sb.WriteByte('"')
			case 'x':
				// \xHH
				if r.pos+2 < len(r.src) {
					n, err := strconv.ParseUint(r.src[r.pos+1:r.pos+3], 16, 32)
					if err == nil {
						sb.WriteRune(rune(n))
						r.pos += 2
					}
				}
			default:
				sb.WriteByte(r.src[r.pos])
			}
			r.pos++
			continue
		}
		sb.WriteByte(c)
		r.pos++
	}
	return 0, &LispError{kind: symParseError, msg: "unterminated string"}
}

func (r *reader) readSharp() (Value, error) {
	// #t #f #\char #(...)  #.  #int32(...)
	r.pos++
	if r.pos >= len(r.src) {
		return 0, &LispError{kind: symParseError, msg: "bad # syntax"}
	}
	c := r.src[r.pos]
	switch {
	case c == 't' && (r.pos+1 >= len(r.src) || !isSymChar(r.src[r.pos+1])):
		r.pos++
		return True, nil
	case c == 'f' && (r.pos+1 >= len(r.src) || !isSymChar(r.src[r.pos+1])):
		r.pos++
		return False, nil
	case c == '\\':
		r.pos++
		start := r.pos
		for r.pos < len(r.src) && isSymChar(r.src[r.pos]) {
			r.pos++
		}
		name := r.src[start:r.pos]
		switch name {
		case "space":
			return CharValue(' '), nil
		case "newline":
			return CharValue('\n'), nil
		case "tab":
			return CharValue('\t'), nil
		}
		rn, _ := utf8.DecodeRuneInString(name)
		return CharValue(rn), nil
	case c == '(':
		lst, err := r.readList('(')
		if err != nil {
			return 0, err
		}
		n := 0
		for p := lst; IsCons(p); p = cdrFast(p) {
			n++
		}
		vec := r.g.NewVector(n, Nil)
		i := 0
		for p := lst; IsCons(p); p = cdrFast(p) {
			vectorData(vec)[i] = carFast(p)
			i++
		}
		return vec, nil
	case c == 'a':
		// #array(...) - approximate as vector
		if strings.HasPrefix(r.src[r.pos:], "array") {
			r.pos += 5
			r.skipWS()
			return r.read()
		}
	case c == '.':
		// read-time eval: just read the form and eval
		r.pos++
		v, err := r.read()
		if err != nil {
			return 0, err
		}
		return r.g.Eval(v)
	}
	// fallback: try reading as symbol/number e.g. #x10
	start := r.pos - 1
	for r.pos < len(r.src) && isSymChar(r.src[r.pos]) {
		r.pos++
	}
	tok := r.src[start:r.pos]
	return r.g.atomFromToken(tok)
}

func (r *reader) readAtom() (Value, error) {
	start := r.pos
	if r.peek() == '|' {
		// |symbol with escapes|
		r.pos++
		var sb strings.Builder
		for r.pos < len(r.src) && r.src[r.pos] != '|' {
			sb.WriteByte(r.src[r.pos])
			r.pos++
		}
		r.pos++
		return SymbolValue(internString(sb.String())), nil
	}
	for r.pos < len(r.src) && isSymChar(r.src[r.pos]) {
		r.pos++
	}
	tok := r.src[start:r.pos]
	return r.g.atomFromToken(tok)
}

func (g *Interp) atomFromToken(tok string) (Value, error) {
	if tok == "" {
		return 0, &LispError{kind: symParseError, msg: "empty token"}
	}
	// number?
	if v, ok := parseNumber(tok); ok {
		return v, nil
	}
	switch tok {
	case ".":
		return 0, &LispError{kind: symParseError, msg: "unexpected ."}
	}
	return SymbolValue(internString(tok)), nil
}

func parseNumber(tok string) (Value, bool) {
	if tok == "" {
		return 0, false
	}
	// hex
	if strings.HasPrefix(tok, "0x") || strings.HasPrefix(tok, "0X") {
		if n, err := strconv.ParseUint(tok[2:], 16, 64); err == nil {
			return FixnumValue(int64(n)), true
		}
		return 0, false
	}
	if n, err := strconv.ParseInt(tok, 10, 64); err == nil {
		if n >= fixnumMin && n <= fixnumMax {
			return FixnumValue(n), true
		}
		if f, err := strconv.ParseFloat(tok, 64); err == nil {
			return DoubleValue(f), true
		}
		return 0, false
	}
	if f, err := strconv.ParseFloat(tok, 64); err == nil {
		if !looksLikeNumber(tok) {
			return 0, false
		}
		return DoubleValue(f), true
	}
	return 0, false
}

func looksLikeNumber(tok string) bool {
	if tok == "" {
		return false
	}
	c := tok[0]
	if c == '+' || c == '-' {
		return len(tok) > 1 && (unicode.IsDigit(rune(tok[1])) || tok[1] == '.')
	}
	return unicode.IsDigit(rune(c)) || (c == '.' && len(tok) > 1 && unicode.IsDigit(rune(tok[1])))
}
