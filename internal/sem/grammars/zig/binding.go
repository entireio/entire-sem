package zig

//#include "tree_sitter/parser.h"
//TSLanguage *tree_sitter_zig();
import "C"

import (
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"
)

// GetLanguage returns the vendored tree-sitter-zig grammar
// (tree-sitter-grammars/tree-sitter-zig v1.1.2, ABI 14), promoting Zig from
// inventory-only to the semantic tier.
func GetLanguage() *sitter.Language {
	return sitter.NewLanguage(unsafe.Pointer(C.tree_sitter_zig()))
}
