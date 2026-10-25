// Package syntax implements Lyth's lexer, abstract syntax tree, and parser.
// It knows nothing about types; later stages record semantic information in
// side tables keyed by AST nodes.
package syntax

import "github.com/lyth-lang/lyth/internal/source"

// Kind is a token kind.
type Kind int

const (
	ILLEGAL Kind = iota
	EOF
	NEWLINE

	IDENT
	INT
	FLOAT
	RUNE
	STRING    // "..." without templates; Value holds the decoded text
	RAWSTRING // `...`; Value holds the text

	TEMPLATE_START      // opening quote of a template string
	TEMPLATE_TEXT       // literal segment; Value holds the decoded text
	TEMPLATE_EXPR_START // ${
	TEMPLATE_EXPR_END   // } closing ${
	TEMPLATE_END        // closing quote

	operatorBegin
	ADD // +
	SUB // -
	MUL // *
	QUO // /
	REM // %
	AND // &
	OR  // |
	XOR // ^
	SHL // <<
	SHR // >>

	LAND // &&
	LOR  // ||
	NOT  // !

	EQL // ==
	NEQ // !=
	LSS // <
	LEQ // <=
	GTR // >
	GEQ // >=

	ASSIGN     // =
	ADD_ASSIGN // +=
	SUB_ASSIGN // -=
	MUL_ASSIGN // *=
	QUO_ASSIGN // /=
	REM_ASSIGN // %=
	AND_ASSIGN // &=
	OR_ASSIGN  // |=
	XOR_ASSIGN // ^=
	SHL_ASSIGN // <<=
	SHR_ASSIGN // >>=
	INC        // ++
	DEC        // --

	AND_NOT        // &^
	AND_NOT_ASSIGN // &^=

	DOT        // .
	SAFE_DOT   // ?.
	QUESTION   // ?
	ELVIS      // ?:
	NOT_NOT    // !!
	RANGE      // ..
	RANGE_EXCL // ..<
	ARROW      // ->
	ELLIPSIS   // ...
	COMMA      // ,
	SEMI       // ;
	COLON      // :
	AT         // @
	DEFINE     // :=

	LPAREN // (
	RPAREN // )
	LBRACK // [
	RBRACK // ]
	LBRACE // {
	RBRACE // }
	operatorEnd

	keywordBegin
	AS
	BREAK
	CATCH
	CHAN
	CONST
	CONTINUE
	DEFER
	ENUM
	ELSE
	FALSE
	FOR
	FUN
	GO
	IF
	IMPORT
	IN
	INTERFACE
	IS
	MAP
	NULL
	PACKAGE
	PRIVATE
	PUB
	RETURN
	SEALED
	SELECT
	STRUCT
	THIS
	THROW
	THROWS
	TRUE
	TYPEALIAS
	VAL
	VAR
	WHEN
	WHILE
	keywordEnd
)

var kindNames = map[Kind]string{
	ILLEGAL: "illegal token", EOF: "end of file", NEWLINE: "newline",
	IDENT: "identifier", INT: "integer literal", FLOAT: "float literal", RUNE: "rune literal",
	STRING: "string literal", RAWSTRING: "raw string literal",
	TEMPLATE_START: "string literal", TEMPLATE_TEXT: "string text", TEMPLATE_EXPR_START: "${",
	TEMPLATE_EXPR_END: "}", TEMPLATE_END: "end of string",
	ADD: "+", SUB: "-", MUL: "*", QUO: "/", REM: "%", AND: "&", OR: "|", XOR: "^", SHL: "<<", SHR: ">>",
	LAND: "&&", LOR: "||", NOT: "!", EQL: "==", NEQ: "!=", LSS: "<", LEQ: "<=", GTR: ">", GEQ: ">=",
	ASSIGN: "=", ADD_ASSIGN: "+=", SUB_ASSIGN: "-=", MUL_ASSIGN: "*=", QUO_ASSIGN: "/=", REM_ASSIGN: "%=",
	AND_ASSIGN: "&=", OR_ASSIGN: "|=", XOR_ASSIGN: "^=", SHL_ASSIGN: "<<=", SHR_ASSIGN: ">>=",
	AND_NOT: "&^", AND_NOT_ASSIGN: "&^=",
	INC: "++", DEC: "--", DOT: ".", SAFE_DOT: "?.", QUESTION: "?", ELVIS: "?:", NOT_NOT: "!!",
	RANGE: "..", RANGE_EXCL: "..<", ARROW: "->", ELLIPSIS: "...", COMMA: ",", SEMI: ";",
	COLON: ":", AT: "@", DEFINE: ":=", LPAREN: "(", RPAREN: ")", LBRACK: "[", RBRACK: "]", LBRACE: "{", RBRACE: "}",
}

var keywords = map[string]Kind{
	"as": AS, "break": BREAK, "catch": CATCH, "chan": CHAN, "const": CONST, "continue": CONTINUE, "defer": DEFER, "enum": ENUM,
	"else": ELSE, "false": FALSE, "for": FOR, "func": FUN, "go": GO, "if": IF, "import": IMPORT,
	"in": IN, "interface": INTERFACE, "is": IS, "map": MAP, "null": NULL, "package": PACKAGE, "private": PRIVATE,
	"pub": PUB, "return": RETURN, "sealed": SEALED, "select": SELECT, "struct": STRUCT, "self": THIS, "throw": THROW, "throws": THROWS,
	"true": TRUE, "typealias": TYPEALIAS, "val": VAL, "var": VAR, "when": WHEN, "while": WHILE,
}

func (k Kind) String() string {
	if k > keywordBegin && k < keywordEnd {
		for s, kk := range keywords {
			if kk == k {
				return s
			}
		}
	}
	if s, ok := kindNames[k]; ok {
		return s
	}
	return "token"
}

// IsKeyword reports whether k is a keyword.
func (k Kind) IsKeyword() bool { return k > keywordBegin && k < keywordEnd }

// LookupKeyword returns the keyword kind for s, or IDENT.
func LookupKeyword(s string) Kind {
	if k, ok := keywords[s]; ok {
		return k
	}
	return IDENT
}

// IsKeywordName reports whether s is a Lyth keyword.
func IsKeywordName(s string) bool {
	_, ok := keywords[s]
	return ok
}

// Token is a lexed token.
type Token struct {
	Kind  Kind
	Text  string // source text
	Value string // decoded value for string-like tokens
	Span  source.Span
}

// Comment is a source comment, kept for doc comments and build constraints.
type Comment struct {
	Text string // including the // or /* */ markers
	Span source.Span
}
