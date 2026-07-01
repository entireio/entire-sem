package erlang

//#include "tree_sitter/parser.h"
//TSLanguage *tree_sitter_erlang();
import "C"

import (
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"
)

// GetLanguage returns the vendored tree-sitter-erlang grammar (WhatsApp,
// tag 0.19, ABI 14), promoting Erlang from inventory-only to the semantic
// tier.
func GetLanguage() *sitter.Language {
	return sitter.NewLanguage(unsafe.Pointer(C.tree_sitter_erlang()))
}
