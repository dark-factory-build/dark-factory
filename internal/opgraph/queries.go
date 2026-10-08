package opgraph

// Tree-sitter queries, one per grammar. They only shape syntax into a small
// common vocabulary; what a shape means for a framework is decided in Go.
//
//	@call, @call.object, @call.name, @call.arg  a call or construction
//	@decorator, .object, .name, .arg            a decorator, annotation or attribute
//	@pair, @pair.key, @pair.value               a keyword argument or object entry
//	@const.name, @const.value                   a binding of a name to a value
//	@definition, .name, .super, .class          a function, method or class
//	@import                                     an imported module
//	@export                                     an exported function (JavaScript)
//	@global, @directive, @string                browser markers, prologues, literals
var queries = map[string]string{
	"javascript": scriptQuery("identifier"),
	"typescript": scriptQuery("type_identifier"),
	"tsx":        scriptQuery("type_identifier"),
	"python": `
(call function: [(identifier) @call.name (attribute object: (_) @call.object attribute: (identifier) @call.name)]) @call
(call arguments: (argument_list (_) @call.arg)) @call
(decorator (call arguments: (argument_list (_) @decorator.arg))) @decorator
(decorator [
  (call function: [(identifier) @decorator.name (attribute object: (_) @decorator.object attribute: (identifier) @decorator.name)])
  (identifier) @decorator.name
  (attribute object: (_) @decorator.object attribute: (identifier) @decorator.name)]) @decorator
(keyword_argument name: (identifier) @pair.key value: (_) @pair.value) @pair
(assignment left: (identifier) @const.name right: (_) @const.value)
(import_statement name: [(dotted_name) @import (aliased_import name: (dotted_name) @import)])
(import_from_statement module_name: (dotted_name) @import)
(function_definition name: (identifier) @definition.name) @definition
(class_definition name: (identifier) @definition.name) @definition @definition.class
(string) @string
`,
	"ruby": `
(call receiver: (_) @call.object method: (_) @call.name) @call
(call !receiver method: (_) @call.name) @call
(call arguments: (argument_list (_) @call.arg)) @call
(pair key: (_) @pair.key value: (_) @pair.value) @pair
(assignment left: [(constant) (identifier)] @const.name right: (_) @const.value)
(class name: (_) @definition.name superclass: (superclass (_) @definition.super)) @definition @definition.class
(class name: (_) @definition.name !superclass) @definition @definition.class
(module name: (_) @definition.name) @definition @definition.class
(method name: (_) @definition.name) @definition
(string) @string
`,
	"java": `
(method_invocation object: (_) @call.object name: (identifier) @call.name) @call
(method_invocation !object name: (identifier) @call.name) @call
(method_invocation arguments: (argument_list (_) @call.arg)) @call
(object_creation_expression type: (_) @call.name) @call
(object_creation_expression arguments: (argument_list (_) @call.arg)) @call
(annotation name: (_) @decorator.name) @decorator
(annotation arguments: (annotation_argument_list (_) @decorator.arg)) @decorator
(marker_annotation name: (_) @decorator.name) @decorator
(element_value_pair key: (identifier) @pair.key value: (_) @pair.value) @pair
(variable_declarator name: (identifier) @const.name value: (_) @const.value)
(import_declaration (scoped_identifier) @import)
(method_declaration name: (identifier) @definition.name) @definition
(class_declaration name: (identifier) @definition.name) @definition @definition.class
(interface_declaration name: (identifier) @definition.name) @definition @definition.class
(string_literal) @string
`,
	"kotlin": `
(call_expression (simple_identifier) @call.name (call_suffix)) @call
(call_expression (navigation_expression (_) @call.object (navigation_suffix (simple_identifier) @call.name)) (call_suffix)) @call
(call_expression (call_suffix (value_arguments (value_argument) @call.arg))) @call
(call_expression (call_expression (simple_identifier) @call.name (call_suffix (value_arguments (value_argument) @call.arg))) (call_suffix (annotated_lambda))) @call
(annotation (user_type (type_identifier) @decorator.name)) @decorator
(annotation (constructor_invocation (user_type (type_identifier) @decorator.name))) @decorator
(annotation (constructor_invocation (value_arguments (value_argument) @decorator.arg))) @decorator
(value_argument (simple_identifier) @pair.key (_) @pair.value) @pair
(property_declaration (variable_declaration (simple_identifier) @const.name) (_) @const.value)
(import_header (identifier) @import)
(function_declaration (simple_identifier) @definition.name) @definition
(class_declaration (type_identifier) @definition.name) @definition @definition.class
(object_declaration (type_identifier) @definition.name) @definition @definition.class
(string_literal) @string
`,
	"rust": `
(call_expression function: [
  (identifier) @call.name
  (scoped_identifier path: (_) @call.object name: (identifier) @call.name)
  (field_expression value: (_) @call.object field: (field_identifier) @call.name)
  (generic_function function: [(identifier) @call.name (scoped_identifier path: (_) @call.object name: (identifier) @call.name) (field_expression value: (_) @call.object field: (field_identifier) @call.name)])]) @call
(call_expression arguments: (arguments (_) @call.arg)) @call
(macro_invocation macro: (identifier) @call.name) @call
(macro_invocation (token_tree (_) @call.arg)) @call
(attribute_item (attribute [(identifier) @decorator.name (scoped_identifier name: (identifier) @decorator.name)])) @decorator
(attribute_item (attribute arguments: (token_tree (_) @decorator.arg))) @decorator
(const_item name: (identifier) @const.name value: (_) @const.value)
(static_item name: (identifier) @const.name value: (_) @const.value)
(let_declaration pattern: (identifier) @const.name value: (_) @const.value)
(use_declaration argument: (_) @import)
(function_item name: (identifier) @definition.name) @definition
(string_literal) @string
(raw_string_literal) @string
`,
}

func scriptQuery(className string) string {
	return `
(call_expression function: [(identifier) @call.name (member_expression object: (_) @call.object property: (property_identifier) @call.name)]) @call
(call_expression arguments: (arguments (_) @call.arg)) @call
(new_expression constructor: [(identifier) @call.name (member_expression object: (_) @call.object property: (property_identifier) @call.name)]) @call
(new_expression arguments: (arguments (_) @call.arg)) @call
(decorator [
  (call_expression function: [(identifier) @decorator.name (member_expression object: (_) @decorator.object property: (property_identifier) @decorator.name)])
  (identifier) @decorator.name]) @decorator
(decorator (call_expression arguments: (arguments (_) @decorator.arg))) @decorator
(pair key: (_) @pair.key value: (_) @pair.value) @pair
(variable_declarator name: (identifier) @const.name value: (_) @const.value)
(import_statement source: (string) @import)
(export_statement source: (string) @import)
(function_declaration name: (identifier) @definition.name) @definition
(method_definition name: (property_identifier) @definition.name) @definition
(class_declaration name: (` + className + `) @definition.name) @definition @definition.class
(variable_declarator name: (identifier) @definition.name value: [(arrow_function) (function_expression)]) @definition
(export_statement declaration: [
  (function_declaration name: (identifier) @export)
  (lexical_declaration (variable_declarator name: (identifier) @export))])
(member_expression object: (identifier) @global)
(program (expression_statement (string) @directive))
[(string) (template_string)] @string
`
}
