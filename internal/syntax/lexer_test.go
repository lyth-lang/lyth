package syntax

import (
	"strings"
	"testing"

	"github.com/lyth-lang/lyth/internal/diag"
	"github.com/lyth-lang/lyth/internal/source"
)

func lexKinds(t *testing.T, src string) ([]string, *diag.Sink) {
	t.Helper()
	sink := &diag.Sink{}
	toks, _ := Lex(source.NewFile("t.lyth", "t.lyth", []byte(src)), sink)
	var out []string
	for _, tk := range toks {
		s := tk.Kind.String()
		switch tk.Kind {
		case IDENT, INT, FLOAT:
			s = tk.Text
		case STRING, TEMPLATE_TEXT, RUNE, RAWSTRING:
			s = tk.Kind.String() + "(" + tk.Value + ")"
		case NEWLINE:
			s = "NL"
		case EOF:
			s = "EOF"
		}
		out = append(out, s)
	}
	return out, sink
}

func TestLexer(t *testing.T) {
	tests := []struct {
		src  string
		want string
	}{
		{"val x = 1", "val x = 1 NL EOF"},
		{"a\n\n\nb", "a NL b NL EOF"},
		{"foo(a,\n b)", "foo ( a , b ) NL EOF"},
		{"[1,\n2]", "[ 1 , 2 ] NL EOF"},
		{"{\na\n}", "{ NL a NL } NL EOF"},
		{"f(\n{ x ->\n x })", "f ( { x -> NL x } ) NL EOF"},
		{"0..<n", "0 ..< n NL EOF"},
		{"1..5", "1 .. 5 NL EOF"},
		{"1.5e3", "1.5e3 NL EOF"},
		{"x?.y ?: z!!", "x ?. y ?: z !! NL EOF"},
		{"f()?", "f ( ) ? NL EOF"},
		{"a??.b", "a ? ?. b NL EOF"},
		{"m<Chan<Int>>", "m < Chan < Int >> NL EOF"},
		{`"hi"`, "string literal(hi) NL EOF"},
		{`"a${x}b"`, "string literal string text(a) ${ x } string text(b) end of string NL EOF"},
		{`"${m["k"]}"`, "string literal ${ m [ string literal(k) ] } end of string NL EOF"},
		{`"\$x \u00e9"`, "string literal($x é) NL EOF"},
		{"`raw\\n`", "raw string literal(raw\\n) NL EOF"},
		{"'a'", "rune literal(a) NL EOF"},
		{"x := 1", "x := 1 NL EOF"},
		{"a // c\nb /* d */ c", "a NL b c NL EOF"},
		{"0x1F 0o17 0b101 1_000", "0x1F 0o17 0b101 1_000 NL EOF"},
		{"a &&\nb", "a && NL b NL EOF"},
	}
	for _, tt := range tests {
		got, sink := lexKinds(t, tt.src)
		if strings.Join(got, " ") != tt.want {
			t.Errorf("Lex(%q)\n got: %s\nwant: %s", tt.src, strings.Join(got, " "), tt.want)
		}
		if sink.HasErrors() {
			t.Errorf("Lex(%q): unexpected diagnostics: %v", tt.src, sink.All()[0].Message)
		}
	}
}

func TestLexerTemplateTextFirst(t *testing.T) {
	got, _ := lexKinds(t, `"Hello, ${user.name}!"`)
	want := "string literal string text(Hello, ) ${ user . name } string text(!) end of string NL EOF"
	if strings.Join(got, " ") != want {
		t.Errorf("got %s\nwant %s", strings.Join(got, " "), want)
	}
}

func TestLexerErrors(t *testing.T) {
	tests := []struct {
		src  string
		code diag.Code
	}{
		{`"abc`, diag.EUnterminated},
		{"017", diag.EInvalidNumber},
		{"1__0", diag.EInvalidNumber},
		{"'ab'", diag.EInvalidChar},
		{"#", diag.EInvalidChar},
		{`"\q"`, diag.EInvalidEscape},
		{"/* x", diag.EUnterminated},
	}
	for _, tt := range tests {
		_, sink := lexKinds(t, tt.src)
		all := sink.All()
		if len(all) == 0 || all[0].Code != tt.code {
			var got diag.Code
			if len(all) > 0 {
				got = all[0].Code
			}
			t.Errorf("Lex(%q): got code %q, want %q", tt.src, got, tt.code)
		}
	}
}

func TestLexerDollarLiteral(t *testing.T) {
	// A '$' not followed by '{' is text: SQL placeholders, and $name, which
	// the checker reports only when name is a variable in scope (ADR 0042).
	for src, want := range map[string]string{
		`"where id = $1 and price > US$ 5"`: "string literal(where id = $1 and price > US$ 5) NL EOF",
		`"$set $ref $body$ \$name"`:         "string literal($set $ref $body$ $name) NL EOF",
	} {
		got, sink := lexKinds(t, src)
		if sink.HasErrors() {
			t.Fatalf("Lex(%s): unexpected error: %s", src, sink.All()[0].Message)
		}
		if strings.Join(got, " ") != want {
			t.Errorf("Lex(%s): got %s", src, strings.Join(got, " "))
		}
	}
}
