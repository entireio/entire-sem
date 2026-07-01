package perl

//#include "tree_sitter/parser.h"
//const TSLanguage *tree_sitter_perl();
import "C"

import (
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"
)

// GetLanguage returns the vendored tree-sitter-perl grammar
// (tree-sitter-perl/tree-sitter-perl, release branch, ABI 14), promoting Perl
// from inventory-only to the semantic tier.
func GetLanguage() *sitter.Language {
	return sitter.NewLanguage(unsafe.Pointer(C.tree_sitter_perl()))
}
