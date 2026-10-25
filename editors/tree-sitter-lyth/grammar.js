/**
 * Tree-sitter grammar for Lyth.
 *
 * Made for editors: highlighting, outline, indentation, bracket matching, text objects and
 * runnables. Declarations (functions and methods, structs, interfaces, enums, type aliases,
 * constants, variables, imports), parameters and fields with their types, calls, member
 * accesses, lambdas, control-flow headers and strings with `${}` templates are recognized;
 * everything else is a token stream grouped by brackets, which keeps the parser small, fast
 * and tolerant of incomplete code. The Lyth compiler has its own complete parser.
 */

// Words that are keywords wherever they appear in code. Declaration keywords (func, struct,
// const, ...) are matched by their declaration rules.
const KEYWORDS = [
  'else', 'in', 'return', 'break', 'continue', 'throw', 'throws', 'catch', 'is', 'as', 'defer', 'go', 'private', 'pub',
];

const PRIMITIVE_TYPES = [
  'int', 'int8', 'int16', 'int32', 'int64', 'uint', 'uint8', 'uint16', 'uint32', 'uint64', 'uintptr',
  'float32', 'float64', 'complex64', 'complex128', 'string', 'bool', 'byte', 'rune', 'any', 'error',
  'comparable', 'unit',
];

const commaSep1 = (rule) => seq(rule, repeat(seq(',', rule)), optional(','));
const commaSep = (rule) => optional(commaSep1(rule));

module.exports = grammar({
  name: 'lyth',

  extras: ($) => [/\s/, $.line_comment, $.block_comment],

  word: ($) => $.identifier,

  conflicts: ($) => [
    [$.type_test, $.operator],
    [$.keyword, $.type_test],
    [$.keyword, $.catch_clause],
    [$._element, $._type],
    [$.tuple_type, $.function_type],
    [$.parenthesized, $.parameters],
    [$.field_declaration, $.typed_local],
    [$.parameter, $.typed_local],
    [$.channel_type],
    [$._atom, $._simple_type, $.qualified_type],
    [$._atom, $.method_spec, $._simple_type],
    [$._atom, $.typed_local, $._simple_type],
    [$._atom, $.short_var_declaration, $._simple_type],
    [$.channel_type, $.operator],
    [$.pointer_type, $.operator],
    [$._atom, $.field_declaration, $.typed_local],
    [$._atom, $.parameter, $.typed_local],
    [$.method_call],
    [$.call_expression],
    [$.struct_declaration],
    [$._simple_type, $.qualified_type],
    [$.function_declaration, $._simple_type],
    [$._atom, $.array_type],
    [$.array_type, $.operator],
    [$.bracketed, $.slice_type],
    [$._atom, $.typed_local, $.lambda_parameter],
    [$._atom, $.short_var_declaration, $.lambda_parameter],
    [$._atom, $.short_var_declaration],
    [$._atom, $.typed_local],
    [$._atom, $.lambda_parameter],
    [$._atom, $.qualified_type],
    [$._atom, $._simple_type],
    [$.typed_local, $.lambda_parameter],
    [$.function_type, $.parenthesized],
  ],

  rules: {
    source_file: ($) => repeat($._element),

    // ------------------------------------------------------------------ comments

    line_comment: (_) => token(seq('//', /[^\n]*/)),
    block_comment: (_) => token(seq('/*', /[^*]*\*+([^/*][^*]*\*+)*/, '/')),

    // ------------------------------------------------------------------ structure

    _element: ($) =>
      choice(
        $.package_clause,
        $.import_declaration,
        $.function_declaration,
        $.struct_declaration,
        $.interface_declaration,
        $.enum_declaration,
        $.type_alias,
        $.const_declaration,
        $.var_declaration,
        $.if_expression,
        $.while_statement,
        $.for_statement,
        $.when_expression,
        $.select_statement,
        $.short_var_declaration,
        $.typed_local,
        $.label,
        $.jump,
        $.catch_clause,
        $.type_test,
        $.call_expression,
        $.method_call,
        $.member_access,
        $.typed_literal,
        $.empty_struct,
        $.map_type,
        $.channel_type,
        $.lambda,
        $.parenthesized,
        $.bracketed,
        $._atom,
      ),

    _atom: ($) =>
      choice(
        $.identifier,
        $.primitive_type,
        $.self,
        $.boolean,
        $.null,
        $.number,
        $.string,
        $.raw_string,
        $.rune,
        $.keyword,
        $.operator,
        ',',
        ';',
        ':',
      ),

    keyword: (_) => choice(...KEYWORDS),

    block: ($) => seq('{', repeat($._element), '}'),
    parenthesized: ($) => seq('(', repeat($._element), ')'),
    bracketed: ($) => seq('[', repeat($._element), ']'),

    // ------------------------------------------------------------------ declarations

    package_clause: ($) => prec.dynamic(10, seq('package', field('name', $.identifier))),

    import_declaration: ($) =>
      prec.dynamic(10, prec.right(seq('import', choice($._import_spec, seq('(', repeat(choice($._import_spec, ';')), ')'))))),

    _import_spec: ($) => prec.right(seq(field('path', $.string), optional(seq('as', field('alias', $.identifier))))),

    // func f(...), func T.m(), func *T.m(), func Page[T].first(), func []T.second[T](), func string.shout()
    function_declaration: ($) =>
      prec.dynamic(
        10,
        prec.right(
          seq(
            'func',
            optional(field('receiver', $.receiver)),
            field('name', $.identifier),
            optional(field('type_parameters', $.type_parameters)),
            field('parameters', $.parameters),
            optional(seq(':', field('result', $._result))),
            optional('throws'),
            optional(field('body', $.block)),
          ),
        ),
      ),

    receiver: ($) => seq(optional('*'), field('type', $._receiver_type), '.'),
    _receiver_type: ($) =>
      choice(
        $._simple_type,
        $.qualified_type,
        $.generic_type,
        $.slice_type,
        $.map_type,
        $.channel_type,
      ),

    type_parameters: ($) => seq('[', commaSep1($.type_parameter), ']'),
    type_parameter: ($) => seq(field('name', alias($.identifier, $.type_identifier)), optional(seq(':', field('constraint', $._type)))),

    parameters: ($) => seq('(', repeat(choice($.parameter, $._element)), ')'),
    parameter: ($) => prec.dynamic(20, seq(field('name', $.identifier), ':', optional('...'), field('type', $._type))),

    _result: ($) => choice($._type, $.tuple_type),
    tuple_type: ($) => prec.dynamic(5, seq('(', commaSep1($._type), ')')),

    struct_declaration: ($) =>
      prec.dynamic(
        10,
        seq(
          'struct',
          field('name', alias($.identifier, $.type_identifier)),
          optional(field('type_parameters', $.type_parameters)),
          optional(seq(':', commaSep1($._type))),
          optional(field('body', $.field_list)),
        ),
      ),
    field_list: ($) => seq('{', repeat(choice($.field_declaration, $._element)), '}'),
    field_declaration: ($) =>
      prec.dynamic(20, prec.right(seq(field('name', $.identifier), ':', field('type', $._type), optional(field('tag', $.raw_string))))),

    interface_declaration: ($) =>
      prec.dynamic(
        10,
        seq(
          optional('sealed'),
          'interface',
          field('name', alias($.identifier, $.type_identifier)),
          optional(field('type_parameters', $.type_parameters)),
          field('body', $.interface_body),
        ),
      ),
    interface_body: ($) => seq('{', repeat(choice($.method_spec, $._type, $._element)), '}'),
    method_spec: ($) =>
      prec.dynamic(
        20,
        prec.right(
          seq(
            field('name', $.identifier),
            optional(field('type_parameters', $.type_parameters)),
            field('parameters', $.parameters),
            optional(seq(':', field('result', $._result))),
            optional('throws'),
          ),
        ),
      ),

    enum_declaration: ($) =>
      prec.dynamic(10, seq('enum', field('name', alias($.identifier, $.type_identifier)), field('body', $.enum_body))),
    enum_body: ($) => seq('{', commaSep($.enum_entry), '}'),
    enum_entry: ($) => field('name', $.identifier),

    type_alias: ($) =>
      prec.dynamic(
        10,
        seq('typealias', field('name', alias($.identifier, $.type_identifier)), optional($.type_parameters), '=', field('type', $._type)),
      ),

    const_declaration: ($) =>
      prec.dynamic(10, prec.right(seq('const', field('name', $.identifier), optional(seq(':', field('type', $._type)))))),

    var_declaration: ($) =>
      prec.dynamic(10, prec.right(seq(choice('var', 'val'), field('name', $.identifier), optional(seq(':', field('type', $._type)))))),

    // x := 1, a, b := f()
    short_var_declaration: ($) =>
      prec.dynamic(8, seq(field('name', $.identifier), repeat(seq(',', field('name', $.identifier))), ':=')),

    // x: T = e, x: T
    typed_local: ($) => prec.dynamic(6, seq(field('name', $.identifier), ':', field('type', $._type))),

    // ------------------------------------------------------------------ types

    _type: ($) =>
      choice(
        $._simple_type,
        $.qualified_type,
        $.generic_type,
        $.pointer_type,
        $.slice_type,
        $.array_type,
        $.map_type,
        $.channel_type,
        $.function_type,
        $.nullable_type,
        $.empty_struct_type,
      ),
    // struct{}: Go's empty struct; struct{}{} is its value.
    empty_struct_type: (_) => prec(2, seq('struct', '{', '}')),
    empty_struct: (_) => prec.dynamic(10, seq('struct', '{', '}', '{', '}')),
    _simple_type: ($) => choice($.primitive_type, alias($.identifier, $.type_identifier)),
    qualified_type: ($) => seq(field('package', $.identifier), '.', field('name', alias($.identifier, $.type_identifier))),
    generic_type: ($) => prec(2, seq(choice($._simple_type, $.qualified_type), field('arguments', $.type_arguments))),
    type_arguments: ($) => seq('[', commaSep1($._type), ']'),
    pointer_type: ($) => prec.right(seq('*', $._type)),
    slice_type: ($) => prec.right(seq('[', ']', $._type)),
    array_type: ($) => prec.right(seq('[', choice($.number, '...', $.identifier), ']', $._type)),
    map_type: ($) => prec.right(seq('map', '[', $._type, ']', $._type)),
    channel_type: ($) => prec.right(choice(seq('chan', optional('<-'), $._type), seq('<-', 'chan', $._type))),
    function_type: ($) => prec.right(seq('(', commaSep($._type), ')', '->', $._type, optional('throws'))),
    nullable_type: ($) => prec.left(3, seq($._type, token.immediate('?'))),

    // ------------------------------------------------------------------ control flow

    // Headers have no trailing lambdas, so the first `{` after them opens the body.
    _header_element: ($) =>
      choice(
        $.identifier,
        $.primitive_type,
        $.self,
        $.boolean,
        $.null,
        $.number,
        $.string,
        $.raw_string,
        $.rune,
        $.keyword,
        $.operator,
        ',',
        ':',
        $.type_test,
        alias($._header_call, $.call_expression),
        alias($._header_method_call, $.method_call),
        $.member_access,
        $.parenthesized,
        $.bracketed,
      ),
    _header_call: ($) => prec(2, seq(field('function', choice($.identifier, $.primitive_type)), $.parenthesized)),
    _header_method_call: ($) => prec(3, seq(choice('.', '?.'), field('name', $.identifier), $.parenthesized)),

    if_expression: ($) => prec.dynamic(10, seq('if', repeat($._header_element), field('body', $.block))),
    while_statement: ($) => prec.dynamic(10, seq('while', repeat($._header_element), field('body', $.block))),
    for_statement: ($) => prec.dynamic(10, seq('for', repeat($._header_element), field('body', $.block))),
    when_expression: ($) => prec.dynamic(10, seq('when', repeat($._header_element), field('body', $.when_body))),
    when_body: ($) => seq('{', repeat($._element), '}'),
    select_statement: ($) => prec.dynamic(10, seq('select', field('body', $.when_body))),

    // f() catch { fallback }, catch (err) { }, catch (e: *NotFound) { }
    catch_clause: ($) =>
      prec.dynamic(
        10,
        seq(
          'catch',
          optional(seq('(', field('binder', $.identifier), optional(seq(':', field('type', $._type))), ')')),
          field('body', $.block),
        ),
      ),

    // x is T, x !is T, x as T, x as? T
    type_test: ($) => prec.dynamic(10, prec.right(seq(choice('is', '!is', 'as', 'as?'), field('type', $._type)))),

    // outer@ for ..., break@outer
    label: ($) => prec.dynamic(10, seq(field('name', $.identifier), token.immediate('@'))),
    jump: ($) => prec.dynamic(10, seq(choice('break', 'continue', 'return'), token.immediate('@'), field('label', $.identifier))),

    // ------------------------------------------------------------------ expressions

    // f(x), User(Name = "a"), int(x), and trailing lambdas: run { }, f(x) { }
    call_expression: ($) =>
      prec.dynamic(
        5,
        prec(2, seq(field('function', choice($.identifier, $.primitive_type)), choice(seq($.parenthesized, optional($.lambda)), $.lambda))),
      ),
    method_call: ($) =>
      prec.dynamic(5, prec(3, seq(choice('.', '?.'), field('name', $.identifier), choice(seq($.parenthesized, optional($.lambda)), $.lambda)))),
    member_access: ($) => prec(1, seq(choice('.', '?.'), field('name', choice($.identifier, $.number)))),

    // []int{1, 2}, map[string]any{"a": 1}, [3]int{1, 2}
    typed_literal: ($) =>
      prec.dynamic(6, seq(field('type', choice($.slice_type, $.array_type, $.map_type)), token.immediate('{'), repeat($._element), '}')),

    // { a, b -> a + b }, { it * 2 }
    lambda: ($) => prec.dynamic(1, seq('{', optional($.lambda_parameters), repeat($._element), '}')),
    lambda_parameters: ($) => prec.dynamic(30, seq(commaSep1($.lambda_parameter), '->')),
    lambda_parameter: ($) => seq(field('name', $.identifier), optional(seq(':', field('type', $._type)))),

    // ------------------------------------------------------------------ tokens

    operator: (_) =>
      choice(
        '+', '-', '*', '/', '%', '=', '==', '!=', '<', '>', '<=', '>=', '&&', '||', '!', '&', '|', '^', '<<', '>>', '&^',
        '+=', '-=', '*=', '/=', '%=', '&=', '|=', '^=', '<<=', '>>=', '&^=', '++', '--', '->', '?', '?:', '!!', '..', '..<', '...',
        '<-', '.', '?.', '!in', '!is', 'as?',
      ),

    identifier: (_) => /[\p{L}_][\p{L}\p{Nd}_]*/u,
    primitive_type: (_) => choice(...PRIMITIVE_TYPES),
    self: (_) => choice('self', 'it'),
    boolean: (_) => choice('true', 'false'),
    null: (_) => 'null',

    number: (_) =>
      token(
        choice(
          /0[xX][0-9a-fA-F_]+(\.[0-9a-fA-F_]*)?([pP][+-]?[0-9_]+)?i?/,
          /0[oO][0-7_]+i?/,
          /0[bB][01_]+i?/,
          /[0-9][0-9_]*(\.[0-9_]*)?([eE][+-]?[0-9_]+)?i?/,
          /\.[0-9][0-9_]*([eE][+-]?[0-9_]+)?i?/,
        ),
      ),

    rune: (_) => token(seq("'", choice(/[^'\\\n]/, /\\[^\n]/, /\\[0-7]{3}/, /\\x[0-9a-fA-F]{2}/, /\\u[0-9a-fA-F]{4}/, /\\U[0-9a-fA-F]{8}/), "'")),

    string: ($) => seq('"', repeat(choice($.string_content, $.escape_sequence, $.interpolation, $.dollar)), token.immediate('"')),
    string_content: (_) => token.immediate(prec(1, /[^"\\$\n]+/)),
    dollar: (_) => token.immediate('$'),
    escape_sequence: (_) =>
      token.immediate(seq('\\', choice(/[^xuU0-7\n]/, /[0-7]{3}/, /x[0-9a-fA-F]{2}/, /u[0-9a-fA-F]{4}/, /U[0-9a-fA-F]{8}/))),
    interpolation: ($) => seq(token.immediate('${'), repeat($._element), '}'),

    raw_string: (_) => token(seq('`', /[^`]*/, '`')),
  },
});
