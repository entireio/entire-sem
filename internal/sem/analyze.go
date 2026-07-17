package sem

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/entireio/entire-graph/internal/gitutil"
)

func AnalyzeGitRange(ctx context.Context, repo, base, head string, paths []string) (Result, error) {
	changed, err := gitutil.ChangedFiles(ctx, repo, base, head, paths)
	if err != nil {
		return Result{}, err
	}
	parser := TreeSitterParser{}
	result := Result{Base: base, Head: head}
	var deltas []*fileDelta
	for _, file := range changed {
		path := file.Path
		oldPath := file.OldPath
		if oldPath == "" {
			oldPath = path
		}
		if !Supported(path) && !Supported(oldPath) {
			continue
		}

		var before, after string
		var beforeOK, afterOK bool
		if file.Status != "A" {
			before, beforeOK, err = gitutil.ShowFile(ctx, repo, base, oldPath)
			if err != nil {
				return Result{}, err
			}
		}
		if file.Status != "D" {
			after, afterOK, err = gitutil.ShowFile(ctx, repo, head, path)
			if err != nil {
				return Result{}, err
			}
		}

		beforeEntities, language, beforeStatus := parser.ParseWithStatus(oldPath, before)
		afterEntities, afterLanguage, afterStatus := parser.ParseWithStatus(path, after)
		if language == "" {
			language = afterLanguage
		}
		// A parse failure on either side degrades the diff. A TOTAL failure
		// (ParseError with ZERO recovered entities) gives compareEntities no
		// signal at all and would make it report every entity on that side as
		// a phantom removed/added, so the delta is skipped and a
		// machine-readable warning is surfaced instead. A PARTIAL recovery
		// (ParseError with some entities extracted) keeps the diff — the
		// recovered changes are real — but is still flagged with a warning,
		// because symbols missing from the recovered set can surface as
		// phantom removed/added. A validly-emptied file (ParseError false) is
		// never suppressed or flagged, so its real removed changes stand.
		afterParseFailed := afterStatus.ParseError && len(afterEntities) == 0
		beforeParseFailed := beforeStatus.ParseError && len(beforeEntities) == 0
		if afterParseFailed || beforeParseFailed {
			status, warnPath := afterStatus, path
			if !afterParseFailed {
				status, warnPath = beforeStatus, oldPath
			}
			result.Warnings = append(result.Warnings, parseFailureWarning(warnPath, status, true))
			continue
		}
		if afterStatus.ParseError || beforeStatus.ParseError {
			status, warnPath := afterStatus, path
			if !afterStatus.ParseError {
				status, warnPath = beforeStatus, oldPath
			}
			result.Warnings = append(result.Warnings, parseFailureWarning(warnPath, status, false))
		}
		if !beforeOK {
			beforeEntities = nil
		}
		if !afterOK {
			afterEntities = nil
		}

		changes, removed, added := compareEntities(beforeEntities, afterEntities)
		if len(changes) == 0 && len(removed) == 0 && len(added) == 0 {
			continue
		}
		deltas = append(deltas, &fileDelta{
			path:     path,
			oldPath:  file.OldPath,
			status:   file.Status,
			language: language,
			changes:  changes,
			removed:  removed,
			added:    added,
		})
	}

	result.Warnings = append(result.Warnings, reconcileMoves(deltas)...)

	for _, delta := range deltas {
		changes := delta.changes
		for _, oldEntity := range delta.removed {
			changes = append(changes, removedChange(oldEntity))
		}
		for _, newEntity := range delta.added {
			changes = append(changes, addedChange(newEntity))
		}
		if len(changes) == 0 {
			continue
		}
		sortChanges(changes)
		result.Files = append(result.Files, FileChange{
			Path:     delta.path,
			OldPath:  delta.oldPath,
			Status:   delta.status,
			Language: delta.language,
			Changes:  changes,
		})
	}

	if err := addDependentCounts(ctx, repo, head, &result); err != nil {
		return Result{}, err
	}
	return result, nil
}

// parseFailureWarning builds the warning emitted when a changed file fails to
// parse on one side of the diff. It reuses the provider path's machine-readable
// codes (parseStatus.ParseError → PartialFailure, see provider.go), and both
// surfaces warn on any ParseError — but the effect wording is diff-specific:
// the provider always emits its (possibly partial) output, while the diff path
// suppresses the file's delta entirely on a total failure (suppressed == true)
// and keeps a possibly-degraded diff on a partial recovery.
func parseFailureWarning(path string, status ParseStatus, suppressed bool) ProviderWarning {
	code := status.Code
	if code == "" {
		code = "E_PARSE_ERROR"
	}
	var effect string
	switch {
	case suppressed && code == "E_PARSE_TIMEOUT":
		effect = "file diff suppressed; changes omitted because parser time budget was exceeded"
	case suppressed:
		effect = "file diff suppressed; changes omitted because the file could not be parsed"
	default:
		effect = "file parsed with syntax errors on one side; diff kept but may be incomplete or contain phantom changes"
	}
	return ProviderWarning{
		Code:                 code,
		Severity:             "warning",
		FilePath:             path,
		EffectOnCompleteness: effect,
		Detail:               status.Detail,
	}
}

// fileDelta accumulates a file's resolved changes plus the removed/added
// entities still eligible for cross-file move reconciliation.
type fileDelta struct {
	path     string
	oldPath  string
	status   string
	language string
	changes  []EntityChange
	removed  []Entity
	added    []Entity
}

// reconcileMoves matches removed entities in one file against added entities in
// another and rewrites unambiguous high-similarity pairs as a single MOVED
// change on the destination file. Ambiguous matches are left as remove/add and
// reported as warnings. Consumed entities are stripped from the deltas.
func reconcileMoves(deltas []*fileDelta) []ProviderWarning {
	type ref struct {
		delta  int
		index  int
		entity Entity
		path   string
	}
	var removed, added []ref
	for di, delta := range deltas {
		for ri, entity := range delta.removed {
			removed = append(removed, ref{delta: di, index: ri, entity: entity, path: delta.path})
		}
		for ai, entity := range delta.added {
			added = append(added, ref{delta: di, index: ai, entity: entity, path: delta.path})
		}
	}

	usedAdded := make([]bool, len(added))
	usedRemoved := make(map[[2]int]bool)
	consumedAdded := make(map[[2]int]bool)
	var warnings []ProviderWarning

	for ri := range removed {
		r := removed[ri]
		bestAi := -1
		bestScore := 0.0
		secondScore := 0.0
		for ai := range added {
			if usedAdded[ai] {
				continue
			}
			a := added[ai]
			if a.path == r.path || a.entity.Kind != r.entity.Kind {
				continue
			}
			score := similarity(r.entity, a.entity)
			if score > bestScore {
				secondScore = bestScore
				bestScore = score
				bestAi = ai
			} else if score > secondScore {
				secondScore = score
			}
		}
		if bestAi < 0 || bestScore < moveThreshold {
			continue
		}
		if secondScore >= moveThreshold && bestScore-secondScore < ambiguityMargin {
			warnings = append(warnings, ProviderWarning{
				Code:                 "W_MOVE_AMBIGUOUS",
				Severity:             "warning",
				FilePath:             r.path,
				EffectOnCompleteness: "symbol move could not be reconciled unambiguously; reported as remove/add",
				Detail:               fmt.Sprintf("%s %s has multiple equally similar destinations", r.entity.Kind, r.entity.Name),
			})
			continue
		}

		a := added[bestAi]
		usedAdded[bestAi] = true
		usedRemoved[[2]int{r.delta, r.index}] = true
		consumedAdded[[2]int{a.delta, a.index}] = true

		change := EntityChange{
			Type:            "moved",
			Kind:            a.entity.Kind,
			Name:            a.entity.Name,
			OldSignature:    r.entity.Signature,
			NewSignature:    a.entity.Signature,
			OldPath:         r.path,
			NewPath:         a.path,
			BeforeStartLine: r.entity.StartLine,
			AfterStartLine:  a.entity.StartLine,
			Similarity:      bestScore,
			Reconciliation:  "MOVED",
		}
		if r.entity.Name != a.entity.Name {
			change.OldName = r.entity.Name
			change.NewName = a.entity.Name
		}
		deltas[a.delta].changes = append(deltas[a.delta].changes, change)
	}

	for di, delta := range deltas {
		if len(usedRemoved) > 0 {
			var keep []Entity
			for ri, entity := range delta.removed {
				if !usedRemoved[[2]int{di, ri}] {
					keep = append(keep, entity)
				}
			}
			delta.removed = keep
		}
		if len(consumedAdded) > 0 {
			var keep []Entity
			for ai, entity := range delta.added {
				if !consumedAdded[[2]int{di, ai}] {
					keep = append(keep, entity)
				}
			}
			delta.added = keep
		}
	}

	return warnings
}

func AnalyzeCheckpoint(ctx context.Context, repo, checkpointID string) (Result, error) {
	head, err := gitutil.FindCommitWithCheckpoint(ctx, repo, checkpointID)
	if err != nil {
		return Result{}, err
	}
	base, err := gitutil.FirstParent(ctx, repo, head)
	if err != nil {
		return Result{}, err
	}
	result, err := AnalyzeGitRange(ctx, repo, base, head, nil)
	if err != nil {
		return Result{}, err
	}
	result.Checkpoint = checkpointID
	return result, nil
}

// Compare reports the entity-level changes between two parses of the same file.
// Removed and added entities that are not reconciled within the file (rename)
// are emitted as plain removed/added changes.
func Compare(before, after []Entity) []EntityChange {
	changes, removed, added := compareEntities(before, after)
	for _, oldEntity := range removed {
		changes = append(changes, removedChange(oldEntity))
	}
	for _, newEntity := range added {
		changes = append(changes, addedChange(newEntity))
	}
	sortChanges(changes)
	return changes
}

// compareEntities diffs two entity sets from the same file. It returns the
// resolved changes (signature/body changes and within-file renames) plus the
// removed and added entities that were not reconciled, sorted deterministically
// so callers can run a cross-file reconciliation pass over the leftovers.
func compareEntities(before, after []Entity) (changes []EntityChange, removed, added []Entity) {
	beforeByKey := map[string]Entity{}
	afterByKey := map[string]Entity{}
	for _, entity := range before {
		beforeByKey[key(entity)] = entity
	}
	for _, entity := range after {
		afterByKey[key(entity)] = entity
	}

	deleted := map[string]Entity{}
	addedByKey := map[string]Entity{}

	for key, oldEntity := range beforeByKey {
		newEntity, ok := afterByKey[key]
		if !ok {
			deleted[key] = oldEntity
			continue
		}
		switch {
		case oldEntity.Signature != newEntity.Signature:
			changes = append(changes, EntityChange{
				Type:            "signature_changed",
				Kind:            oldEntity.Kind,
				Name:            oldEntity.Name,
				OldSignature:    oldEntity.Signature,
				NewSignature:    newEntity.Signature,
				BeforeStartLine: oldEntity.StartLine,
				AfterStartLine:  newEntity.StartLine,
			})
		case oldEntity.BodyHash != newEntity.BodyHash:
			changes = append(changes, EntityChange{
				Type:            "body_changed",
				Kind:            oldEntity.Kind,
				Name:            oldEntity.Name,
				BeforeStartLine: oldEntity.StartLine,
				AfterStartLine:  newEntity.StartLine,
			})
		}
	}
	for key, newEntity := range afterByKey {
		if _, ok := beforeByKey[key]; !ok {
			addedByKey[key] = newEntity
		}
	}

	for oldKey, oldEntity := range deleted {
		bestKey, bestEntity, score := bestRename(oldEntity, addedByKey)
		if score >= renameThreshold {
			delete(deleted, oldKey)
			delete(addedByKey, bestKey)
			changes = append(changes, EntityChange{
				Type:            "renamed",
				Kind:            oldEntity.Kind,
				Name:            bestEntity.Name,
				OldName:         oldEntity.Name,
				NewName:         bestEntity.Name,
				OldSignature:    oldEntity.Signature,
				NewSignature:    bestEntity.Signature,
				BeforeStartLine: oldEntity.StartLine,
				AfterStartLine:  bestEntity.StartLine,
				Similarity:      score,
				Reconciliation:  "RENAMED",
			})
		}
	}

	removed = sortedEntities(deleted)
	added = sortedEntities(addedByKey)
	return changes, removed, added
}

const (
	renameThreshold = 0.92
	moveThreshold   = 0.92
	// ambiguityMargin marks a move as ambiguous when a second candidate scores
	// within this distance of the best one, so we report remove/add and warn
	// rather than guessing.
	ambiguityMargin = 0.05
)

func removedChange(oldEntity Entity) EntityChange {
	return EntityChange{
		Type:            "removed",
		Kind:            oldEntity.Kind,
		Name:            oldEntity.Name,
		OldSignature:    oldEntity.Signature,
		BeforeStartLine: oldEntity.StartLine,
	}
}

func addedChange(newEntity Entity) EntityChange {
	return EntityChange{
		Type:           "added",
		Kind:           newEntity.Kind,
		Name:           newEntity.Name,
		NewSignature:   newEntity.Signature,
		AfterStartLine: newEntity.StartLine,
	}
}

func sortedEntities(byKey map[string]Entity) []Entity {
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Entity, 0, len(byKey))
	for _, k := range keys {
		out = append(out, byKey[k])
	}
	return out
}

func sortChanges(changes []EntityChange) {
	sort.Slice(changes, func(i, j int) bool {
		left := lineForSort(changes[i])
		right := lineForSort(changes[j])
		if left == right {
			return fmt.Sprintf("%s:%s", changes[i].Kind, changes[i].Name) < fmt.Sprintf("%s:%s", changes[j].Kind, changes[j].Name)
		}
		return left < right
	})
}

func key(entity Entity) string {
	return entity.Kind + ":" + entity.Name
}

func lineForSort(change EntityChange) int {
	if change.AfterStartLine > 0 {
		return change.AfterStartLine
	}
	return change.BeforeStartLine
}

func bestRename(old Entity, added map[string]Entity) (string, Entity, float64) {
	var bestKey string
	var best Entity
	var bestScore float64
	for key, candidate := range added {
		if candidate.Kind != old.Kind {
			continue
		}
		score := similarity(old, candidate)
		if score > bestScore {
			bestKey = key
			best = candidate
			bestScore = score
		}
	}
	return bestKey, best, bestScore
}

func similarity(a, b Entity) float64 {
	if a.Fingerprint != "" && a.Fingerprint == b.Fingerprint {
		return 1
	}
	if a.BodyHash != "" && a.BodyHash == b.BodyHash {
		return 0.97
	}
	return jaccard(a.Signature, b.Signature)
}

func jaccard(a, b string) float64 {
	left := tokenSet(a)
	right := tokenSet(b)
	if len(left) == 0 && len(right) == 0 {
		return 1
	}
	var intersection int
	for token := range left {
		if right[token] {
			intersection++
		}
	}
	union := len(left) + len(right) - intersection
	if union == 0 {
		return 0
	}
	return math.Round((float64(intersection)/float64(union))*100) / 100
}

func tokenSet(value string) map[string]bool {
	out := map[string]bool{}
	token := ""
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			token += string(r)
			continue
		}
		if token != "" {
			out[token] = true
			token = ""
		}
	}
	if token != "" {
		out[token] = true
	}
	return out
}
