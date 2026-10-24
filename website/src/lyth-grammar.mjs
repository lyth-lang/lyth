// A TextMate grammar for Lyth, used by the site's code highlighter. Lyth is spelled like Go
// wherever Go has the concept, so tokens Go also has get the scopes of the Go grammar the
// Go tabs use, and the two sides of a comparison are colored alike. Scopes end in `.lyth`,
// except a few operators that the themes color specially for Go.

const ident = String.raw`[A-Za-z_]\w*`;
// a type: `int`, `*User`, `[]User?`, `map[string]int`, `chan<- T`, `errors.Kind`, `Page[T]`
const type = String.raw`(?:\*|\[\d*\])*(?:map\[[^\]\n]+\])?(?:<-\s*)?(?:chan\b(?:\s*<-)?\s*)?(?:\*|\[\d*\])*${ident}(?:\.${ident})?(?:\[[^\]\n]*\])?\??`;

const typeParts = [
	{ include: '#type-keywords' },
	{ include: '#storage-types' },
	{ match: String.raw`\b${ident}\b`, name: 'entity.name.type.lyth' },
	{ match: String.raw`\*`, name: 'keyword.operator.address.go' },
	{ match: String.raw`\?`, name: 'keyword.operator.optional.lyth' },
	{ match: String.raw`<-`, name: 'keyword.operator.channel.lyth' },
];

export default {
	name: 'lyth',
	displayName: 'Lyth',
	scopeName: 'source.lyth',
	patterns: [{ include: '#statements' }],
	repository: {
		statements: {
			patterns: [
				{ include: '#comments' },
				{ include: '#strings' },
				{ include: '#numbers' },
				{ include: '#func-declaration' },
				{ include: '#type-declaration' },
				{ include: '#labels' },
				{ include: '#typed-names' },
				{ include: '#result-type' },
				{ include: '#type-tests' },
				{ include: '#keywords' },
				{ include: '#type-keywords' },
				{ include: '#storage-types' },
				{ include: '#constants' },
				{ include: '#builtin-functions' },
				{ include: '#function-calls' },
				{ include: '#variables' },
				{ include: '#operators' },
				{ include: '#braces' },
			],
		},
		comments: {
			patterns: [
				{
					begin: String.raw`/\*`,
					end: String.raw`\*/`,
					name: 'comment.block.lyth',
					captures: { 0: { name: 'punctuation.definition.comment.lyth' } },
				},
				{
					begin: '//',
					end: '$',
					name: 'comment.line.double-slash.lyth',
					beginCaptures: { 0: { name: 'punctuation.definition.comment.lyth' } },
				},
			],
		},
		strings: {
			patterns: [
				{
					begin: '"',
					end: '"',
					name: 'string.quoted.double.lyth',
					beginCaptures: { 0: { name: 'punctuation.definition.string.begin.lyth' } },
					endCaptures: { 0: { name: 'punctuation.definition.string.end.lyth' } },
					patterns: [{ include: '#escapes' }, { include: '#template' }],
				},
				{
					begin: '`',
					end: '`',
					name: 'string.quoted.raw.lyth',
					beginCaptures: { 0: { name: 'punctuation.definition.string.begin.lyth' } },
					endCaptures: { 0: { name: 'punctuation.definition.string.end.lyth' } },
				},
				{
					match: String.raw`'(?:[^'\\]|\\(?:[0-7]{3}|['"\\abfnrtv]|x\h{2}|u\h{4}|U\h{8}))'`,
					name: 'string.quoted.rune.lyth',
				},
			],
		},
		escapes: {
			match: String.raw`\\(?:[0-7]{3}|["'\\abfnrtv$]|x\h{2}|u\h{4}|U\h{8})`,
			name: 'constant.character.escape.lyth',
		},
		// "${expr}": the expression is colored as code
		template: {
			begin: String.raw`\$\{`,
			end: String.raw`\}`,
			beginCaptures: { 0: { name: 'punctuation.section.embedded.begin.lyth' } },
			endCaptures: { 0: { name: 'punctuation.section.embedded.end.lyth' } },
			contentName: 'meta.embedded.line.lyth',
			patterns: [{ include: '#statements' }],
		},
		// nested braces, so that a lambda inside a template doesn't end it
		braces: {
			begin: String.raw`\{`,
			end: String.raw`\}`,
			patterns: [{ include: '#statements' }],
		},
		numbers: {
			patterns: [
				{ match: String.raw`\b0[xX][\h_]+\b`, name: 'constant.numeric.hexadecimal.lyth' },
				{ match: String.raw`\b0[bB][01_]+\b`, name: 'constant.numeric.binary.lyth' },
				{ match: String.raw`\b0[oO][0-7_]+\b`, name: 'constant.numeric.octal.lyth' },
				// `1..10` is a range, not a float
				{ match: String.raw`\b\d[\d_]*(?:\.(?!\.)\d[\d_]*)?(?:[eE][+-]?\d+)?\b`, name: 'constant.numeric.decimal.lyth' },
			],
		},
		// func name, func T.name, func *T.name, func []T.name[T], func Page[T].name, func Max[T: cmp.Ordered]
		'func-declaration': {
			match: String.raw`\b(func)\b\s*(?:((?:\*|\[\])*${ident}(?:\[[^\]\n]*\])?)\s*(\.)\s*)?(${ident})?(\[[^\]\n]*\])?`,
			captures: {
				1: { name: 'keyword.function.lyth' },
				2: { patterns: typeParts },
				3: { name: 'punctuation.other.period.lyth' },
				4: { name: 'entity.name.function.lyth' },
				5: { patterns: [{ include: '#type-parameters' }] },
			},
		},
		// struct User, sealed interface Shape, enum Color, typealias Handler, struct Circle : Shape
		'type-declaration': {
			match: String.raw`\b(?:(sealed)\s+)?(struct|interface|enum|typealias)\s+(${ident})(?:(\[[^\]\n]*\]))?(?:\s*(:)\s*((?:${type})(?:\s*,\s*${type})*))?`,
			captures: {
				1: { name: 'storage.modifier.lyth' },
				2: { patterns: [{ include: '#type-keywords' }] },
				3: { name: 'entity.name.type.lyth' },
				4: { patterns: [{ include: '#type-parameters' }] },
				5: { name: 'punctuation.other.colon.lyth' },
				6: { patterns: typeParts },
			},
		},
		// [T], [K: comparable, V], [T: cmp.Ordered]
		'type-parameters': {
			patterns: [...typeParts, { match: ':', name: 'punctuation.other.colon.lyth' }],
		},
		// outer@ for, break@outer, continue@outer
		labels: {
			patterns: [
				{
					match: String.raw`\b(break|continue)(@)(${ident})`,
					captures: {
						1: { name: 'keyword.control.lyth' },
						2: { name: 'punctuation.definition.label.lyth' },
						3: { name: 'variable.other.label.lyth' },
					},
				},
				{
					match: String.raw`\b(${ident})(@)(?=\s*(?:for|while)\b)`,
					captures: {
						1: { name: 'variable.other.label.lyth' },
						2: { name: 'punctuation.definition.label.lyth' },
					},
				},
			],
		},
		// name: Type in parameters, fields, locals and type parameters
		'typed-names': {
			match: String.raw`\b(${ident})\s*(:)(?![:=])\s*(${type})`,
			captures: {
				1: { name: 'variable.parameter.lyth' },
				2: { name: 'punctuation.other.colon.lyth' },
				3: { patterns: typeParts },
			},
		},
		// ): Type
		'result-type': {
			match: String.raw`(\))\s*(:)\s*(${type})`,
			captures: {
				2: { name: 'punctuation.other.colon.lyth' },
				3: { patterns: typeParts },
			},
		},
		// is T, !is T, as T, as? T
		'type-tests': {
			match: String.raw`(!?\bis|\bas\??)\s+(${type})`,
			captures: {
				1: { name: 'keyword.control.lyth' },
				2: { patterns: typeParts },
			},
		},
		keywords: {
			patterns: [
				{
					match: String.raw`\b(break|catch|continue|defer|else|for|go|if|in|is|as|return|select|throw|when|while)\b`,
					name: 'keyword.control.lyth',
				},
				{ match: String.raw`!(?:in|is)\b`, name: 'keyword.control.lyth' },
				{ match: String.raw`\bfunc\b`, name: 'keyword.function.lyth' },
				{ match: String.raw`\bvar\b`, name: 'keyword.var.lyth' },
				{ match: String.raw`\bconst\b`, name: 'keyword.const.lyth' },
				{ match: String.raw`\bimport\b`, name: 'keyword.control.import.lyth' },
				{
					match: String.raw`\b(package)\s+(${ident})`,
					captures: { 1: { name: 'keyword.package.lyth' }, 2: { name: 'entity.name.type.package.lyth' } },
				},
				{ match: String.raw`\b(throws|sealed)\b`, name: 'storage.modifier.lyth' },
			],
		},
		'type-keywords': {
			patterns: [
				{ match: String.raw`\bstruct\b`, name: 'keyword.struct.lyth' },
				{ match: String.raw`\binterface\b`, name: 'keyword.interface.lyth' },
				{ match: String.raw`\b(enum|typealias)\b`, name: 'keyword.type.lyth' },
				{ match: String.raw`\bmap\b`, name: 'keyword.map.lyth' },
				{ match: String.raw`\bchan\b`, name: 'keyword.channel.lyth' },
			],
		},
		'storage-types': {
			patterns: [
				{ match: String.raw`\bbool\b`, name: 'storage.type.boolean.lyth' },
				{ match: String.raw`\bbyte\b`, name: 'storage.type.byte.lyth' },
				{ match: String.raw`\berror\b`, name: 'storage.type.error.lyth' },
				{ match: String.raw`\b(?:complex(?:64|128)|float(?:32|64)|u?int(?:8|16|32|64)?)\b`, name: 'storage.type.numeric.lyth' },
				{ match: String.raw`\brune\b`, name: 'storage.type.rune.lyth' },
				{ match: String.raw`\bstring\b`, name: 'storage.type.string.lyth' },
				{ match: String.raw`\buintptr\b`, name: 'storage.type.uintptr.lyth' },
				{ match: String.raw`\bunit\b`, name: 'storage.type.unit.lyth' },
				{ match: String.raw`\bany\b`, name: 'entity.name.type.any.lyth' },
				{ match: String.raw`\bcomparable\b`, name: 'entity.name.type.comparable.lyth' },
			],
		},
		constants: {
			patterns: [
				{ match: String.raw`\b(true|false)\b`, name: 'constant.language.boolean.lyth' },
				{ match: String.raw`\bnull\b`, name: 'constant.language.null.lyth' },
				{ match: String.raw`\bself\b`, name: 'variable.language.self.lyth' },
			],
		},
		'builtin-functions': {
			match: String.raw`\b(append|cap|clear|close|complex|copy|delete|imag|len|make|max|min|new|panic|print|println|real|recover)\b(?=\s*\()`,
			name: 'entity.name.function.support.builtin.lyth',
		},
		// f(x), pkg.F(x), Max[int](xs)
		'function-calls': {
			match: String.raw`\b(${ident})(?=\s*(?:\[[^\]\n]*\]\s*)?\()`,
			captures: { 1: { name: 'entity.name.function.lyth' } },
		},
		variables: {
			patterns: [
				// a, b := f() and x = y, also named arguments
				{ match: String.raw`\b${ident}(?=\s*(?:,\s*${ident}\s*)*(?::=|(?:<<|>>|[-+*/%&|^])?=(?!=)))`, name: 'variable.other.assignment.lyth' },
				{ match: String.raw`\b${ident}\b`, name: 'variable.other.lyth' },
			],
		},
		operators: {
			patterns: [
				{ match: String.raw`\?\.|\?:|!!|\?`, name: 'keyword.operator.optional.lyth' },
				{ match: String.raw`->`, name: 'keyword.operator.arrow.lyth' },
				{ match: String.raw`\.\.<|\.\.(?!\.)`, name: 'keyword.operator.range.lyth' },
				{ match: String.raw`\.\.\.`, name: 'keyword.operator.ellipsis.lyth' },
				{ match: String.raw`&&|\|\||!(?!=)`, name: 'keyword.operator.logical.lyth' },
				{ match: String.raw`==|!=|<=|>=|<(?![<=])|>(?![>=])`, name: 'keyword.operator.comparison.lyth' },
				{ match: String.raw`\+\+`, name: 'keyword.operator.increment.lyth' },
				{ match: String.raw`--`, name: 'keyword.operator.decrement.lyth' },
				{ match: String.raw`(?:<<|>>|[-+*/%&|^])?=|:=`, name: 'keyword.operator.assignment.go' },
				{ match: String.raw`&(?=\s*${ident})`, name: 'keyword.operator.address.go' },
				{ match: String.raw`<<|>>|[&|^]`, name: 'keyword.operator.arithmetic.bitwise.lyth' },
				{ match: String.raw`[-+*/%]`, name: 'keyword.operator.arithmetic.go' },
				{ match: String.raw`\.`, name: 'punctuation.other.period.lyth' },
				{ match: ',', name: 'punctuation.other.comma.lyth' },
				{ match: ':', name: 'punctuation.other.colon.lyth' },
			],
		},
	},
};
