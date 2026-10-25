(function_declaration) @function.around
(function_declaration
  body: (block "{" (_)* @function.inside "}"))

(lambda) @function.around

(struct_declaration) @class.around
(struct_declaration
  body: (field_list "{" (_)* @class.inside "}"))
(interface_declaration) @class.around
(interface_declaration
  body: (interface_body "{" (_)* @class.inside "}"))
(enum_declaration) @class.around


(line_comment)+ @comment.around
(block_comment) @comment.around
