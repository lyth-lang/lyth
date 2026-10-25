(struct_declaration
  "struct" @context
  name: (type_identifier) @name) @item

(field_declaration
  name: (identifier) @name) @item

(interface_declaration
  "interface" @context
  name: (type_identifier) @name) @item

(method_spec
  name: (identifier) @name) @item

(enum_declaration
  "enum" @context
  name: (type_identifier) @name) @item

(enum_entry
  name: (identifier) @name) @item

(function_declaration
  "func" @context
  receiver: (receiver)? @context
  name: (identifier) @name) @item

(type_alias
  ["type" "typealias"] @context
  name: (type_identifier) @name) @item

(type_definition
  "type" @context
  name: (type_identifier) @name) @item

(const_declaration
  "const" @context
  name: (identifier) @name) @item

(var_declaration
  name: (identifier) @name) @item
