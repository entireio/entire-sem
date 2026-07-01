package clojure

//#include "tree_sitter/parser.h"
//TSLanguage *tree_sitter_clojure();
import "C"

import (
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"
)

// GetLanguage returns the vendored tree-sitter-clojure grammar (sogaiu
// v0.0.13, ABI 14), promoting Clojure from inventory-only to the semantic
// tier.
func GetLanguage() *sitter.Language {
	return sitter.NewLanguage(unsafe.Pointer(C.tree_sitter_clojure()))
}
