package objc

//#include "tree_sitter/parser.h"
//const TSLanguage *tree_sitter_objc();
import "C"

import (
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"
)

// GetLanguage returns the vendored tree-sitter-objc grammar
// (amaanq/tree-sitter-objc v3.0.2, ABI 14), promoting Objective-C from
// inventory-only to the semantic tier.
func GetLanguage() *sitter.Language {
	return sitter.NewLanguage(unsafe.Pointer(C.tree_sitter_objc()))
}
