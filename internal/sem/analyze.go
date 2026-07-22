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

		// Support is content-aware: extensionless executables can still route to a
		// parser through their shebang. Classify each existing side independently
		// so a rename across the parser boundary cannot become a one-sided phantom
		// remove/add. Any unsupported side suppresses the delta and leaves a
		// machine-readable completeness marker instead.
		_, beforeSupported := languageForContent(oldPath, before)
		_, afterSupported := languageForContent(path, after)
		beforeUnsupported := beforeOK && !beforeSupported
		afterUnsupported := afterOK && !afterSupported
		if beforeUnsupported || afterUnsupported {
			warningPath := path
			detail := "head version has no supported parser"
			if beforeUnsupported && !afterUnsupported {
				warningPath = oldPath
				detail = "base version has no supported parser"
			} else if beforeUnsupported && afterUnsupported {
				detail = "base and head versions have no supported parser"
			}
			effect := "file skipped; no parser for this file type, so its changes are not analyzed"
			if beforeOK && afterOK && beforeUnsupported != afterUnsupported {
				effect = "file diff suppressed; one side has no parser, so changes cannot be compared safely"
			}
			result.Warnings = append(result.Warnings, ProviderWarning{
				Code:                 "W_UNSUPPORTED_FILE",
				Severity:             "info",
				FilePath:             warningPath,
				EffectOnCompleteness: effect,
				Detail:               detail,
			})
			continue
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
	beforeByKey, afterByKey := keyedEntityMaps(before, after)

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
		left, right := changes[i], changes[j]
		if leftLine, rightLine := lineForSort(left), lineForSort(right); leftLine != rightLine {
			return leftLine < rightLine
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		if left.Type != right.Type {
			return left.Type < right.Type
		}
		if left.OldSignature != right.OldSignature {
			return left.OldSignature < right.OldSignature
		}
		if left.NewSignature != right.NewSignature {
			return left.NewSignature < right.NewSignature
		}
		if left.BeforeStartLine != right.BeforeStartLine {
			return left.BeforeStartLine < right.BeforeStartLine
		}
		if left.AfterStartLine != right.AfterStartLine {
			return left.AfterStartLine < right.AfterStartLine
		}
		if left.OldName != right.OldName {
			return left.OldName < right.OldName
		}
		if left.NewName != right.NewName {
			return left.NewName < right.NewName
		}
		if left.OldPath != right.OldPath {
			return left.OldPath < right.OldPath
		}
		if left.NewPath != right.NewPath {
			return left.NewPath < right.NewPath
		}
		if left.Reconciliation != right.Reconciliation {
			return left.Reconciliation < right.Reconciliation
		}
		if left.Similarity != right.Similarity {
			return left.Similarity < right.Similarity
		}
		return left.DependentsCount < right.DependentsCount
	})
}

// keyedEntityMaps assigns each entity an ephemeral key such that entities that
// should be diffed against each other receive the same key on both sides. The
// keys are opaque: they are used only inside compareEntities/bestRename and are
// never persisted or emitted.
//
// Matching is evidence-first within each Kind:Name group:
//
//  1. Exact-signature entities pair as content multisets: combined body and
//     fingerprint evidence first, then body hash, then fingerprint. Members
//     of an equal-content class are interchangeable, so repeated hashes pair
//     safely up to the count present on both sides. This keeps an inserted
//     exact-signature duplicate from shifting every surviving duplicate.
//
//  2. Remaining exact signatures pair by occurrence before leftovers take
//     unambiguous fingerprint/body anchors across signatures. This prevents
//     copied or swapped bodies from stealing an unchanged signature, while the
//     identity anchors preserve the right survivor when one overload is removed
//     as another changes signature. A final positional fallback still reports
//     an otherwise-unanchored in-place signature edit as signature_changed
//     rather than remove+add (issue #35). Multiple residuals without identity
//     evidence remain inherently ambiguous; their positional pairing is
//     retained as a compatibility heuristic.
//
// Numeric pair keys and side-specific unmatched keys avoid delimiter aliasing
// with source-language names and signatures.
func keyedEntityMaps(before, after []Entity) (map[string]Entity, map[string]Entity) {
	type groupKey struct {
		kind string
		name string
	}
	type entityGroup struct {
		before []int
		after  []int
	}

	groups := map[groupKey]*entityGroup{}
	groupFor := func(entity Entity) *entityGroup {
		key := groupKey{kind: entity.Kind, name: entity.Name}
		group := groups[key]
		if group == nil {
			group = &entityGroup{}
			groups[key] = group
		}
		return group
	}
	for i, entity := range before {
		group := groupFor(entity)
		group.before = append(group.before, i)
	}
	for i, entity := range after {
		group := groupFor(entity)
		group.after = append(group.after, i)
	}

	groupKeys := make([]groupKey, 0, len(groups))
	for key := range groups {
		groupKeys = append(groupKeys, key)
	}
	sort.Slice(groupKeys, func(i, j int) bool {
		if groupKeys[i].kind != groupKeys[j].kind {
			return groupKeys[i].kind < groupKeys[j].kind
		}
		return groupKeys[i].name < groupKeys[j].name
	})

	beforeByKey := make(map[string]Entity, len(before))
	afterByKey := make(map[string]Entity, len(after))
	beforeMatched := make([]bool, len(before))
	afterMatched := make([]bool, len(after))
	pairCount := 0

	pair := func(beforeIndex, afterIndex int) {
		key := fmt.Sprintf("=%09d", pairCount)
		pairCount++
		beforeMatched[beforeIndex] = true
		afterMatched[afterIndex] = true
		beforeByKey[key] = before[beforeIndex]
		afterByKey[key] = after[afterIndex]
	}
	type evidenceKey struct {
		signature   string
		bodyHash    string
		fingerprint string
	}
	matchClass := func(group *entityGroup, keyFor func(Entity) (evidenceKey, bool)) {
		afterBuckets := map[evidenceKey][]int{}
		for _, afterIndex := range group.after {
			if afterMatched[afterIndex] {
				continue
			}
			key, ok := keyFor(after[afterIndex])
			if ok {
				afterBuckets[key] = append(afterBuckets[key], afterIndex)
			}
		}
		afterOffsets := map[evidenceKey]int{}
		for _, beforeIndex := range group.before {
			if beforeMatched[beforeIndex] {
				continue
			}
			key, ok := keyFor(before[beforeIndex])
			if !ok {
				continue
			}
			offset := afterOffsets[key]
			if offset >= len(afterBuckets[key]) {
				continue
			}
			pair(beforeIndex, afterBuckets[key][offset])
			afterOffsets[key]++
		}
	}
	matchUnique := func(group *entityGroup, keyFor func(Entity) (evidenceKey, bool)) {
		beforeCounts := map[evidenceKey]int{}
		afterCounts := map[evidenceKey]int{}
		afterIndexByKey := map[evidenceKey]int{}
		for _, beforeIndex := range group.before {
			if beforeMatched[beforeIndex] {
				continue
			}
			if key, ok := keyFor(before[beforeIndex]); ok {
				beforeCounts[key]++
			}
		}
		for _, afterIndex := range group.after {
			if afterMatched[afterIndex] {
				continue
			}
			if key, ok := keyFor(after[afterIndex]); ok {
				afterCounts[key]++
				afterIndexByKey[key] = afterIndex
			}
		}
		for _, beforeIndex := range group.before {
			if beforeMatched[beforeIndex] {
				continue
			}
			key, ok := keyFor(before[beforeIndex])
			if ok && beforeCounts[key] == 1 && afterCounts[key] == 1 {
				pair(beforeIndex, afterIndexByKey[key])
			}
		}
	}
	matchRemaining := func(group *entityGroup) {
		afterRemaining := make([]int, 0, len(group.after))
		for _, afterIndex := range group.after {
			if !afterMatched[afterIndex] {
				afterRemaining = append(afterRemaining, afterIndex)
			}
		}
		afterOffset := 0
		for _, beforeIndex := range group.before {
			if beforeMatched[beforeIndex] || afterOffset >= len(afterRemaining) {
				continue
			}
			pair(beforeIndex, afterRemaining[afterOffset])
			afterOffset++
		}
	}

	for _, key := range groupKeys {
		group := groups[key]
		matchClass(group, func(entity Entity) (evidenceKey, bool) {
			ok := entity.BodyHash != "" && entity.Fingerprint != ""
			return evidenceKey{signature: entity.Signature, bodyHash: entity.BodyHash, fingerprint: entity.Fingerprint}, ok
		})
		matchClass(group, func(entity Entity) (evidenceKey, bool) {
			return evidenceKey{signature: entity.Signature, bodyHash: entity.BodyHash}, entity.BodyHash != ""
		})
		matchClass(group, func(entity Entity) (evidenceKey, bool) {
			return evidenceKey{signature: entity.Signature, fingerprint: entity.Fingerprint}, entity.Fingerprint != ""
		})
		matchClass(group, func(entity Entity) (evidenceKey, bool) {
			return evidenceKey{signature: entity.Signature}, true
		})
		matchUnique(group, func(entity Entity) (evidenceKey, bool) {
			ok := entity.BodyHash != "" && entity.Fingerprint != ""
			return evidenceKey{bodyHash: entity.BodyHash, fingerprint: entity.Fingerprint}, ok
		})
		matchUnique(group, func(entity Entity) (evidenceKey, bool) {
			return evidenceKey{fingerprint: entity.Fingerprint}, entity.Fingerprint != ""
		})
		matchUnique(group, func(entity Entity) (evidenceKey, bool) {
			return evidenceKey{bodyHash: entity.BodyHash}, entity.BodyHash != ""
		})
		matchRemaining(group)
	}

	for i, entity := range before {
		if !beforeMatched[i] {
			beforeByKey[fmt.Sprintf("-before:%09d", i)] = entity
		}
	}
	for i, entity := range after {
		if !afterMatched[i] {
			afterByKey[fmt.Sprintf("+after:%09d", i)] = entity
		}
	}
	return beforeByKey, afterByKey
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
