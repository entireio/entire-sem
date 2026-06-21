package zsh

//#include "tree_sitter/parser.h"
//const TSLanguage *tree_sitter_zsh(void);
import "C"
import (
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"
)

func GetLanguage() *sitter.Language {
	ptr := unsafe.Pointer(C.tree_sitter_zsh())
	return sitter.NewLanguage(ptr)
}
