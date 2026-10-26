package syntax

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
)

// Lex tokenizes a whole file. It never fails: malformed input produces
// ILLEGAL tokens and diagnostics so the parser can continue.
func Lex(file *source.File, sink *diag.Sink) ([]Token, []Comment) {
	l := &lexer{file: file, src: file.Content, sink: sink}
	if len(l.src) >= 3 && l.src[0] == 0xEF && l.src[1] == 0xBB && l.src[2] == 0xBF {
		l.errorf(diag.EInvalidChar, 0, 3, "byte order mark is not allowed in Lyth source files")
		l.off = 3
	}
	for {
		if !l.scan() {
			break
		}
	}
	return l.toks, l.comments
}

type lexer struct {
	file     *source.File
	src      []byte
	off      int
	sink     *diag.Sink
	toks     []Token
	comments []Comment
	// stack of open brackets: '(' '[' '{' and 't' for a template expression.
	stack []byte
}

func (l *lexer) span(start, end int) source.Span {
	return source.Span{File: l.file, Start: start, End: end}
}

func (l *lexer) errorf(code diag.Code, start, end int, format string, args ...any) *diag.Diagnostic {
	return l.sink.Errorf(code, l.span(start, end), format, args...)
}

func (l *lexer) emit(kind Kind, start, end int, value string) {
	l.toks = append(l.toks, Token{Kind: kind, Text: string(l.src[start:end]), Value: value, Span: l.span(start, end)})
}

func (l *lexer) emitNewline(at int) {
	if len(l.toks) == 0 {
		return
	}
	last := l.toks[len(l.toks)-1].Kind
	if last == NEWLINE || last == SEMI {
		return
	}
	if n := len(l.stack); n > 0 && (l.stack[n-1] == '(' || l.stack[n-1] == '[') {
		return
	}
	l.toks = append(l.toks, Token{Kind: NEWLINE, Text: "\n", Span: l.span(at, at+1)})
}

func (l *lexer) peekByte(i int) byte {
	if l.off+i < len(l.src) {
		return l.src[l.off+i]
	}
	return 0
}

// scan scans one token (or a whole string literal) and appends it.
// It returns false after emitting EOF.
func (l *lexer) scan() bool {
	if !l.skipSpace() {
		if n := len(l.toks); n > 0 && l.toks[n-1].Kind != NEWLINE {
			// Terminate the last statement.
			l.toks = append(l.toks, Token{Kind: NEWLINE, Text: "", Span: l.span(len(l.src), len(l.src))})
		}
		l.toks = append(l.toks, Token{Kind: EOF, Span: l.span(len(l.src), len(l.src))})
		return false
	}
	before := l.off
	l.scanOne()
	if l.off == before {
		// Defensive: never loop on input the scanners did not consume.
		l.errorf(diag.EInvalidChar, before, before+1, "invalid character")
		l.off++
	}
	return true
}

// skipSpace skips whitespace and comments, emitting NEWLINE tokens. It
// returns false at end of input.
func (l *lexer) skipSpace() bool {
	for l.off < len(l.src) {
		c := l.src[l.off]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			l.off++
		case c == '\n':
			l.emitNewline(l.off)
			l.off++
		case c == '/' && l.peekByte(1) == '/':
			start := l.off
			for l.off < len(l.src) && l.src[l.off] != '\n' {
				l.off++
			}
			l.comments = append(l.comments, Comment{Text: string(l.src[start:l.off]), Span: l.span(start, l.off)})
		case c == '/' && l.peekByte(1) == '*':
			start := l.off
			l.off += 2
			closed := false
			hasNL := false
			for l.off < len(l.src) {
				if l.src[l.off] == '*' && l.peekByte(1) == '/' {
					l.off += 2
					closed = true
					break
				}
				if l.src[l.off] == '\n' {
					hasNL = true
				}
				l.off++
			}
			if !closed {
				l.errorf(diag.EUnterminated, start, start+2, "unterminated block comment")
			}
			l.comments = append(l.comments, Comment{Text: string(l.src[start:l.off]), Span: l.span(start, l.off)})
			if hasNL {
				l.emitNewline(start)
			}
		default:
			return true
		}
	}
	return false
}

// scanOne scans the token starting at l.off, which must not be whitespace.
func (l *lexer) scanOne() {
	start := l.off
	c := l.src[l.off]
	switch {
	case c < utf8.RuneSelf && isLetter(rune(c)) || c >= utf8.RuneSelf && l.isLetterAt(l.off):
		l.scanIdent()
	case isDigit(c):
		l.scanNumber()
	case c == '"':
		l.scanString()
	case c == '`':
		l.scanRawString()
	case c == '\'':
		l.scanRune()
	default:
		l.scanOperator(start)
	}
}

func (l *lexer) isLetterAt(off int) bool {
	r, _ := utf8.DecodeRune(l.src[off:])
	return isLetter(r)
}

func isLetter(r rune) bool {
	return r == '_' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || r >= utf8.RuneSelf && unicode.IsLetter(r)
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

func (l *lexer) scanIdent() {
	start := l.off
	for l.off < len(l.src) {
		r, size := utf8.DecodeRune(l.src[l.off:])
		if isLetter(r) || r < utf8.RuneSelf && isDigit(byte(r)) || r >= utf8.RuneSelf && unicode.IsDigit(r) {
			l.off += size
			continue
		}
		break
	}
	text := string(l.src[start:l.off])
	kind := LookupKeyword(text)
	l.emit(kind, start, l.off, text)
}

func (l *lexer) scanNumber() {
	start := l.off
	kind := INT
	digits := func(valid func(byte) bool) {
		for l.off < len(l.src) && (valid(l.src[l.off]) || l.src[l.off] == '_') {
			l.off++
		}
	}
	isDec := func(c byte) bool { return isDigit(c) }
	isHex := func(c byte) bool { return isDigit(c) || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F' }
	isOct := func(c byte) bool { return '0' <= c && c <= '7' }
	isBin := func(c byte) bool { return c == '0' || c == '1' }

	if l.src[l.off] == '0' && (l.peekByte(1) == 'x' || l.peekByte(1) == 'X') {
		l.off += 2
		digits(isHex)
	} else if l.src[l.off] == '0' && (l.peekByte(1) == 'o' || l.peekByte(1) == 'O') {
		l.off += 2
		digits(isOct)
	} else if l.src[l.off] == '0' && (l.peekByte(1) == 'b' || l.peekByte(1) == 'B') {
		l.off += 2
		digits(isBin)
	} else {
		digits(isDec)
		// A '.' belongs to the number only when a digit follows ("1..5" is a range).
		if l.off < len(l.src) && l.src[l.off] == '.' && isDigit(l.peekByte(1)) {
			kind = FLOAT
			l.off++
			digits(isDec)
		}
		if l.off < len(l.src) && (l.src[l.off] == 'e' || l.src[l.off] == 'E') {
			save := l.off
			l.off++
			if l.off < len(l.src) && (l.src[l.off] == '+' || l.src[l.off] == '-') {
				l.off++
			}
			if l.off < len(l.src) && isDigit(l.src[l.off]) {
				kind = FLOAT
				digits(isDec)
			} else {
				l.off = save
			}
		}
	}
	// Trailing letters or digits make the literal invalid ("12abc", "0x").
	bad := false
	for l.off < len(l.src) {
		r, size := utf8.DecodeRune(l.src[l.off:])
		if isLetter(r) || r < utf8.RuneSelf && isDigit(byte(r)) {
			l.off += size
			bad = true
			continue
		}
		break
	}
	text := string(l.src[start:l.off])
	if bad || !validNumber(text, kind) {
		d := l.errorf(diag.EInvalidNumber, start, l.off, "invalid number literal %s", text)
		lower := strings.ToLower(text)
		switch {
		case strings.Contains(text, "_") && !bad:
			d.WithNote("an underscore must stand between two digits: 1_000")
		case lower == "0x" || lower == "0o" || lower == "0b":
			d.WithNote("%s needs digits after it: 0x1F, 0o17, 0b101", lower)
		case bad:
			d.WithNote("a number cannot be followed directly by letters or other digits")
		case len(text) > 1 && text[0] == '0' && isDigit(text[1]):
			d.WithNote("decimal literals cannot have leading zeros; use 0o17 for octal, 0x1F for hex, 0b101 for binary")
		}
		l.emit(ILLEGAL, start, l.off, text)
		return
	}
	l.emit(kind, start, l.off, text)
}

func validNumber(text string, kind Kind) bool {
	if strings.Contains(text, "__") || strings.HasSuffix(text, "_") {
		return false
	}
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "0x") || strings.HasPrefix(lower, "0o") || strings.HasPrefix(lower, "0b") {
		rest := strings.TrimPrefix(text[2:], "_")
		return rest != "" && !strings.HasPrefix(rest, "_")
	}
	// Decimal: no leading zero unless the literal is "0" or a float like 0.5.
	if kind == INT && len(text) > 1 && text[0] == '0' {
		return false
	}
	if kind == FLOAT && len(text) > 1 && text[0] == '0' && isDigit(text[1]) {
		return false
	}
	for i := 0; i+1 < len(text); i++ {
		if text[i] == '_' && !isDigit(text[i+1]) || text[i+1] == '_' && !isDigit(text[i]) {
			return false
		}
	}
	return true
}

// scanEscape decodes an escape sequence starting at l.off (at the backslash).
// It returns the decoded text and whether it was valid.
func (l *lexer) scanEscape(quote byte) (string, bool) {
	start := l.off
	l.off++ // backslash
	if l.off >= len(l.src) {
		l.errorf(diag.EUnterminated, start, l.off, "unterminated escape sequence")
		return "", false
	}
	c := l.src[l.off]
	l.off++
	switch c {
	case 'n':
		return "\n", true
	case 'r':
		return "\r", true
	case 't':
		return "\t", true
	case 'a':
		return "\a", true
	case 'b':
		return "\b", true
	case 'f':
		return "\f", true
	case 'v':
		return "\v", true
	case '\\':
		return "\\", true
	case '"':
		return "\"", true
	case '\'':
		return "'", true
	case '$':
		return "$", true
	case '0':
		return "\x00", true
	case 'x', 'u', 'U':
		n := map[byte]int{'x': 2, 'u': 4, 'U': 8}[c]
		if l.off+n > len(l.src) {
			l.errorf(diag.EInvalidEscape, start, len(l.src), "invalid escape sequence")
			l.off = len(l.src)
			return "", false
		}
		var v rune
		for i := 0; i < n; i++ {
			d := l.src[l.off+i]
			var x byte
			switch {
			case isDigit(d):
				x = d - '0'
			case 'a' <= d && d <= 'f':
				x = d - 'a' + 10
			case 'A' <= d && d <= 'F':
				x = d - 'A' + 10
			default:
				l.errorf(diag.EInvalidEscape, start, l.off+i+1, "invalid character %q in escape sequence", d)
				l.off += i
				return "", false
			}
			v = v*16 + rune(x)
		}
		l.off += n
		if c == 'x' {
			if quote == '\'' {
				return string(v), true // in a rune literal, \xHH is the code point HH, as in Go
			}
			return string([]byte{byte(v)}), true
		}
		if !utf8.ValidRune(v) {
			l.errorf(diag.EInvalidEscape, start, l.off, "escape sequence is not a valid Unicode code point")
			return "", false
		}
		return string(v), true
	default:
		l.errorf(diag.EInvalidEscape, start, l.off, "unknown escape sequence \\%c", c).
			WithNote("valid escapes are \\n \\r \\t \\a \\b \\f \\v \\\\ \\\" \\' \\$ \\0 \\xHH \\uXXXX \\UXXXXXXXX")
		return "", false
	}
}

// scanString scans a "..." literal. Without templates it emits one STRING
// token; with templates it emits TEMPLATE_START, TEMPLATE_TEXT,
// TEMPLATE_EXPR_START, <expression tokens>, TEMPLATE_EXPR_END, ..., TEMPLATE_END.
func (l *lexer) scanString() {
	start := l.off
	l.off++ // opening quote
	var text strings.Builder
	segStart := l.off
	isTemplate := false
	flush := func(end int) {
		if !isTemplate {
			return
		}
		if text.Len() > 0 {
			l.emit(TEMPLATE_TEXT, segStart, end, text.String())
		}
		text.Reset()
	}
	for {
		if l.off >= len(l.src) || l.src[l.off] == '\n' {
			l.errorf(diag.EUnterminated, start, l.off, "unterminated string literal").
				WithHint("strings cannot span lines; use \\n or a raw `...` string")
			if isTemplate {
				flush(l.off)
				l.emit(TEMPLATE_END, l.off, l.off, "")
			} else {
				l.emit(STRING, start, l.off, text.String())
			}
			return
		}
		c := l.src[l.off]
		switch {
		case c == '"':
			l.off++
			if isTemplate {
				flush(l.off - 1)
				l.emit(TEMPLATE_END, l.off-1, l.off, "")
			} else {
				l.emit(STRING, start, l.off, text.String())
			}
			return
		case c == '\\':
			s, _ := l.scanEscape('"')
			text.WriteString(s)
		case c == '$' && l.peekByte(1) == '{':
			if !isTemplate {
				isTemplate = true
				l.emit(TEMPLATE_START, start, start+1, "")
			}
			flush(l.off)
			exprStart := l.off
			l.off += 2
			l.emit(TEMPLATE_EXPR_START, exprStart, l.off, "")
			l.stack = append(l.stack, 't')
			depth := len(l.stack)
			for len(l.stack) >= depth {
				if l.off >= len(l.src) || l.src[l.off] == '\n' {
					l.errorf(diag.EUnterminated, exprStart, l.off, "unterminated ${ in string template")
					l.stack = l.stack[:depth-1]
					break
				}
				l.scanTemplateToken()
			}
			segStart = l.off
		case c == '$':
			// $name is text; the checker reports it when name is a variable
			// in scope, Kotlin's template habit (E0006, ADR 0042).
			text.WriteByte('$')
			l.off++
		default:
			r, size := utf8.DecodeRune(l.src[l.off:])
			if r == utf8.RuneError && size == 1 {
				l.errorf(diag.EInvalidChar, l.off, l.off+1, "invalid UTF-8 in string literal")
			}
			text.WriteString(string(l.src[l.off : l.off+size]))
			l.off += size
		}
	}
}

// scanTemplateToken scans one token inside ${ ... }, handling the closing brace.
func (l *lexer) scanTemplateToken() {
	for l.off < len(l.src) && (l.src[l.off] == ' ' || l.src[l.off] == '\t') {
		l.off++
	}
	if l.off >= len(l.src) || l.src[l.off] == '\n' {
		return
	}
	if l.src[l.off] == '}' && l.stack[len(l.stack)-1] == 't' {
		l.emit(TEMPLATE_EXPR_END, l.off, l.off+1, "")
		l.stack = l.stack[:len(l.stack)-1]
		l.off++
		return
	}
	before := l.off
	l.scanOne()
	if l.off == before {
		l.errorf(diag.EInvalidChar, before, before+1, "invalid character")
		l.off++
	}
}

func (l *lexer) scanRawString() {
	start := l.off
	l.off++
	for l.off < len(l.src) && l.src[l.off] != '`' {
		l.off++
	}
	if l.off >= len(l.src) {
		l.errorf(diag.EUnterminated, start, start+1, "unterminated raw string literal")
		l.emit(RAWSTRING, start, l.off, string(l.src[start+1:l.off]))
		return
	}
	l.off++
	val := strings.ReplaceAll(string(l.src[start+1:l.off-1]), "\r", "")
	l.emit(RAWSTRING, start, l.off, val)
}

func (l *lexer) scanRune() {
	start := l.off
	l.off++
	var val string
	n := 0
	bad := false
	for l.off < len(l.src) && l.src[l.off] != '\'' && l.src[l.off] != '\n' {
		if l.src[l.off] == '\\' {
			s, ok := l.scanEscape('\'')
			bad = bad || !ok
			val += s
		} else {
			_, size := utf8.DecodeRune(l.src[l.off:])
			val += string(l.src[l.off : l.off+size])
			l.off += size
		}
		n++
	}
	if l.off >= len(l.src) || l.src[l.off] != '\'' {
		l.errorf(diag.EUnterminated, start, l.off, "unterminated rune literal")
		l.emit(ILLEGAL, start, l.off, val)
		return
	}
	l.off++
	if bad {
		l.emit(ILLEGAL, start, l.off, val)
		return
	}
	if utf8.RuneCountInString(val) != 1 {
		l.errorf(diag.EInvalidChar, start, l.off, "rune literal must contain exactly one character").
			WithHint("use double quotes for strings: \"%s\"", val)
		l.emit(ILLEGAL, start, l.off, val)
		return
	}
	l.emit(RUNE, start, l.off, val)
}

func (l *lexer) scanOperator(start int) {
	c := l.src[l.off]
	two := func(k Kind) { l.off += 2; l.emit(k, start, l.off, "") }
	one := func(k Kind) { l.off++; l.emit(k, start, l.off, "") }
	three := func(k Kind) { l.off += 3; l.emit(k, start, l.off, "") }
	n1, n2 := l.peekByte(1), l.peekByte(2)
	switch c {
	case '+':
		switch n1 {
		case '+':
			two(INC)
		case '=':
			two(ADD_ASSIGN)
		default:
			one(ADD)
		}
	case '-':
		switch n1 {
		case '-':
			two(DEC)
		case '=':
			two(SUB_ASSIGN)
		case '>':
			two(ARROW)
		default:
			one(SUB)
		}
	case '*':
		if n1 == '=' {
			two(MUL_ASSIGN)
		} else {
			one(MUL)
		}
	case '/':
		if n1 == '=' {
			two(QUO_ASSIGN)
		} else {
			one(QUO)
		}
	case '%':
		if n1 == '=' {
			two(REM_ASSIGN)
		} else {
			one(REM)
		}
	case '&':
		switch {
		case n1 == '&':
			two(LAND)
		case n1 == '=':
			two(AND_ASSIGN)
		case n1 == '^' && n2 == '=':
			three(AND_NOT_ASSIGN)
		case n1 == '^':
			two(AND_NOT)
		default:
			one(AND)
		}
	case '|':
		switch n1 {
		case '|':
			two(LOR)
		case '=':
			two(OR_ASSIGN)
		default:
			one(OR)
		}
	case '^':
		if n1 == '=' {
			two(XOR_ASSIGN)
		} else {
			one(XOR)
		}
	case '<':
		switch {
		case n1 == '<' && n2 == '=':
			three(SHL_ASSIGN)
		case n1 == '<':
			two(SHL)
		case n1 == '=':
			two(LEQ)
		default:
			one(LSS)
		}
	case '>':
		switch {
		case n1 == '>' && n2 == '=':
			three(SHR_ASSIGN)
		case n1 == '>':
			two(SHR)
		case n1 == '=':
			two(GEQ)
		default:
			one(GTR)
		}
	case '=':
		if n1 == '=' {
			two(EQL)
		} else {
			one(ASSIGN)
		}
	case '!':
		switch n1 {
		case '=':
			two(NEQ)
		case '!':
			two(NOT_NOT)
		default:
			one(NOT)
		}
	case '.':
		switch {
		case n1 == '.' && n2 == '<':
			three(RANGE_EXCL)
		case n1 == '.' && n2 == '.':
			three(ELLIPSIS)
		case n1 == '.':
			two(RANGE)
		default:
			one(DOT)
		}
	case '?':
		switch {
		case n1 == '.' && n2 == '.':
			one(QUESTION) // f()?..<n: a ? before a range
		case n1 == '.':
			two(SAFE_DOT)
		case n1 == ':':
			two(ELVIS)
		default:
			one(QUESTION)
		}
	case ':':
		if n1 == '=' {
			two(DEFINE)
		} else {
			one(COLON)
		}
	case ',':
		one(COMMA)
	case ';':
		one(SEMI)
	case '@':
		one(AT)
	case '(':
		l.stack = append(l.stack, '(')
		one(LPAREN)
	case '[':
		l.stack = append(l.stack, '[')
		one(LBRACK)
	case '{':
		l.stack = append(l.stack, '{')
		one(LBRACE)
	case ')', ']', '}':
		want := map[byte]byte{')': '(', ']': '[', '}': '{'}[c]
		// Pop to the matching bracket if present; tolerate mismatches.
		for i := len(l.stack) - 1; i >= 0; i-- {
			if l.stack[i] == want {
				l.stack = l.stack[:i]
				break
			}
			if l.stack[i] == 't' {
				break
			}
		}
		switch c {
		case ')':
			one(RPAREN)
		case ']':
			one(RBRACK)
		default:
			one(RBRACE)
		}
	default:
		r, size := utf8.DecodeRune(l.src[l.off:])
		l.off += size
		l.errorf(diag.EInvalidChar, start, l.off, "invalid character %q", r)
		l.emit(ILLEGAL, start, l.off, "")
	}
}
