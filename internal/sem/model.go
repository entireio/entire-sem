package sem

import "io"

type Entity struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Signature   string `json:"signature"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	BodyHash    string `json:"body_hash"`
	Fingerprint string `json:"fingerprint"`
	// Local marks a callable defined inside another function (a nested/closure
	// def). It is still a real symbol, but it is only callable from within its
	// enclosing function, so call resolution must not name-match it across scopes.
	Local bool `json:"-"`
}

type EntityChange struct {
	Type            string  `json:"type"`
	Kind            string  `json:"kind"`
	Name            string  `json:"name"`
	OldName         string  `json:"old_name,omitempty"`
	NewName         string  `json:"new_name,omitempty"`
	OldSignature    string  `json:"old_signature,omitempty"`
	NewSignature    string  `json:"new_signature,omitempty"`
	OldPath         string  `json:"old_path,omitempty"`
	NewPath         string  `json:"new_path,omitempty"`
	BeforeStartLine int     `json:"before_start_line,omitempty"`
	AfterStartLine  int     `json:"after_start_line,omitempty"`
	DependentsCount int     `json:"dependents_count"`
	Similarity      float64 `json:"similarity,omitempty"`
	// Reconciliation carries explicit identity-continuity metadata when a
	// delete+add pair was reconciled to a single change: RENAMED (same file),
	// MOVED (across files), or RECONCILED_FROM. Empty for ordinary changes.
	Reconciliation string `json:"reconciliation,omitempty"`
}

type FileChange struct {
	Path     string         `json:"path"`
	OldPath  string         `json:"old_path,omitempty"`
	Status   string         `json:"status"`
	Language string         `json:"language,omitempty"`
	Changes  []EntityChange `json:"changes"`
}

type Result struct {
	Checkpoint string            `json:"checkpoint,omitempty"`
	Base       string            `json:"base"`
	Head       string            `json:"head"`
	Files      []FileChange      `json:"files"`
	Warnings   []ProviderWarning `json:"warnings,omitempty"`
}

type Parser interface {
	Parse(path, content string) ([]Entity, string)
}

func WriteText(out io.Writer, result Result) {
	writeText(out, result)
}
