; Lyth syntax highlighting. Later patterns win, so general rules come first.

(identifier) @variable
(type_identifier) @type
(primitive_type) @type.builtin
((identifier) @constant
  (#match? @constant "^[A-Z][A-Z0-9_]+$"))

; ------------------------------------------------------------------ keywords

(keyword) @keyword

[
  "func" "struct" "interface" "sealed" "enum" "typealias" "const" "var" "val" "import" "package" "as"
  "if" "while" "for" "when" "select" "catch" "is" "!is" "as?" "throws" "map" "chan"
  "break" "continue" "return"
] @keyword

(self) @variable.builtin
(boolean) @boolean
(null) @constant.builtin

; ------------------------------------------------------------------ literals

(number) @number
(rune) @string
(string) @string
(raw_string) @string
(escape_sequence) @escape
(dollar) @string

; ------------------------------------------------------------------ comments

(line_comment) @comment
(block_comment) @comment

; ------------------------------------------------------------------ punctuation and operators

(operator) @operator
["(" ")" "[" "]" "{" "}"] @punctuation.bracket
["," ";" ":" "." "?."] @punctuation.delimiter

; ------------------------------------------------------------------ declarations and uses

(function_declaration name: (identifier) @function)
(method_spec name: (identifier) @function.method)
(parameter name: (identifier) @variable.parameter)
(lambda_parameter name: (identifier) @variable.parameter)
(catch_clause binder: (identifier) @variable.parameter)
(field_declaration name: (identifier) @property)
(field_declaration tag: (raw_string) @string.special)
(enum_entry name: (identifier) @constant)
(const_declaration name: (identifier) @constant)
(import_declaration path: (string) @string.special)
(qualified_type package: (identifier) @variable)

(label name: (identifier) @label)
(jump label: (identifier) @label)

(member_access name: (identifier) @property)
(call_expression function: (identifier) @function.call)
(call_expression function: (primitive_type) @type.builtin)
(method_call name: (identifier) @function.method)
((call_expression function: (identifier) @function.builtin)
  (#any-of? @function.builtin "println" "print" "len" "cap" "make" "append" "copy" "delete" "clear" "min" "max" "panic" "recover" "new"))

; ------------------------------------------------------------------ string templates

(interpolation "${" @punctuation.special "}" @punctuation.special)
