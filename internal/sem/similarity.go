package sem

import (
	"hash/fnv"
	"regexp"
	"sort"
	"strings"
)

// Near-clone detection via MinHash + LSH over normalized symbol bodies. The aim
// is high-confidence near-duplicates (copy-paste with light edits), so tiny
// bodies are suppressed and only pairs above a similarity threshold are emitted.
const (
	minHashCount     = 64
	lshBands         = 16
	lshRows          = minHashCount / lshBands // 4
	shingleSize      = 3
	minShingles      = 8    // suppress boilerplate / tiny functions
	similarThreshold = 0.82 // estimated Jaccard required to emit SIMILAR_TO
	// maxBucketMembers bounds the all-pairs expansion within one LSH band bucket.
	// A bucket larger than this means a body is mass-duplicated across the repo
	// (generated getters, boilerplate, minified code); enumerating its O(k^2) pairs
	// is the dominant cost on large repos and yields only redundant clone-cluster
	// noise, so such buckets are skipped. Genuine small clone groups are unaffected.
	maxBucketMembers = 64
	// maxSimilarityCandidates bounds the TOTAL candidate-pair set across all bands.
	// The per-bucket cap bounds each near-dup cluster, but a repo with thousands of
	// small near-identical clusters (e.g. microsoft/TypeScript's tests/cases corpus)
	// can still accumulate enough pairs to exhaust memory. Past this many candidates
	// the clone-hint signal is long since saturated, so further pairs are pure noise
	// and memory pressure; stop collecting. Bucket iteration is sorted so the chosen
	// subset is deterministic.
	maxSimilarityCandidates = 2_000_000
)

var (
	simHashA   [minHashCount]uint64
	simHashB   [minHashCount]uint64
	simTokenRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*|[^\sA-Za-z0-9_]`)
)

func init() {
	// Deterministic MinHash coefficients so signatures are stable across runs.
	state := uint64(0x9e3779b97f4a7c15)
	for i := 0; i < minHashCount; i++ {
		simHashA[i] = splitmix64(&state) | 1 // odd multiplier
		simHashB[i] = splitmix64(&state)
	}
}

func splitmix64(state *uint64) uint64 {
	*state += 0x9e3779b97f4a7c15
	z := *state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// similarityRelations finds near-duplicate symbol bodies and emits SIMILAR_TO
// edges between them. Only function/method bodies with enough shingles are
// considered. Signatures are computed per symbol and the shingle sets are
// discarded immediately, so memory stays bounded on large repositories.
func similarityRelations(recordsByFile map[string][]SymbolRecord, readContent contentReader) []RelationRecord {
	type sig struct {
		id        string
		signature [minHashCount]uint64
	}
	var sigs []sig

	paths := make([]string, 0, len(recordsByFile))
	for path := range recordsByFile {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		content, ok := readContent(path)
		if !ok {
			continue
		}
		lines := strings.Split(content, "\n")
		for _, symbol := range recordsByFile[path] {
			if symbol.Kind != "function" && symbol.Kind != "method" {
				continue
			}
			shingles := bodyShingles(symbolBlockFromLines(lines, symbol))
			if len(shingles) < minShingles {
				continue
			}
			sigs = append(sigs, sig{id: symbol.ID, signature: minHashSignature(shingles)})
		}
	}
	if len(sigs) < 2 {
		return nil
	}

	// LSH: group by per-band signature slices; symbols sharing a bucket in any
	// band become candidate pairs.
	candidates := map[[2]int]struct{}{}
	capped := false
	for band := 0; band < lshBands && !capped; band++ {
		buckets := map[uint64][]int{}
		for idx := range sigs {
			key := bandKey(sigs[idx].signature, band)
			buckets[key] = append(buckets[key], idx)
		}
		// Iterate buckets in a deterministic order so that, if the global cap
		// truncates the candidate set, the retained subset is reproducible.
		keys := make([]uint64, 0, len(buckets))
		for key := range buckets {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		for _, key := range keys {
			group := buckets[key]
			if len(group) > maxBucketMembers {
				continue // mass-duplication bucket: skip its O(k^2) pairs (noise + the explosion source)
			}
			for i := 0; i < len(group); i++ {
				for j := i + 1; j < len(group); j++ {
					a, b := group[i], group[j]
					if a > b {
						a, b = b, a
					}
					candidates[[2]int{a, b}] = struct{}{}
				}
			}
			if len(candidates) >= maxSimilarityCandidates {
				capped = true // total clone-hint budget reached: stop (bounds memory)
				break
			}
		}
	}

	pairs := make([][2]int, 0, len(candidates))
	for pair := range candidates {
		pairs = append(pairs, pair)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})

	var relations []RelationRecord
	for _, pair := range pairs {
		score := signatureSimilarity(sigs[pair[0]].signature, sigs[pair[1]].signature)
		if score < similarThreshold {
			continue
		}
		from, to := sigs[pair[0]].id, sigs[pair[1]].id
		if from > to {
			from, to = to, from
		}
		relations = append(relations, RelationRecord{
			RecordType:    "relation",
			FromID:        from,
			ToID:          to,
			Type:          "SIMILAR_TO",
			Confidence:    round2(score),
			Reason:        "near-duplicate symbol body (MinHash estimate)",
			RelationScope: "workspace",
			Resolution:    "pattern",
			TargetKind:    "symbol",
			WarningCodes:  []string{},
		})
	}
	return relations
}

// maxMinifiedLineLen: a line longer than this marks a file as minified/bundled.
// Real source lines are short (rarely > a few hundred chars); minified assets pack
// the whole program onto one line of tens of thousands of characters.
const maxMinifiedLineLen = 5000

// longestLineLen returns the length (in bytes) of the longest line, scanning once
// and stopping early once the minified threshold is exceeded.
func longestLineLen(content string) int {
	longest, cur := 0, 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			if cur > longest {
				longest = cur
			}
			if longest > maxMinifiedLineLen {
				return longest
			}
			cur = 0
		} else {
			cur++
		}
	}
	if cur > longest {
		longest = cur
	}
	return longest
}

func bodyShingles(block string) map[uint64]struct{} {
	stripped := strings.ToLower(stripCodeLiteralsAndComments(block))
	tokens := simTokenRe.FindAllString(stripped, -1)
	if len(tokens) < shingleSize {
		return nil
	}
	shingles := map[uint64]struct{}{}
	for i := 0; i+shingleSize <= len(tokens); i++ {
		h := fnv.New64a()
		for j := 0; j < shingleSize; j++ {
			h.Write([]byte(tokens[i+j]))
			h.Write([]byte{0})
		}
		shingles[h.Sum64()] = struct{}{}
	}
	return shingles
}

func minHashSignature(shingles map[uint64]struct{}) [minHashCount]uint64 {
	var sig [minHashCount]uint64
	for i := range sig {
		sig[i] = ^uint64(0)
	}
	for shingle := range shingles {
		for i := 0; i < minHashCount; i++ {
			h := simHashA[i]*shingle + simHashB[i]
			if h < sig[i] {
				sig[i] = h
			}
		}
	}
	return sig
}

func bandKey(sig [minHashCount]uint64, band int) uint64 {
	h := fnv.New64a()
	var buf [8]byte
	for row := 0; row < lshRows; row++ {
		v := sig[band*lshRows+row]
		for b := 0; b < 8; b++ {
			buf[b] = byte(v >> (8 * b))
		}
		h.Write(buf[:])
	}
	return h.Sum64()
}

func round2(value float64) float64 {
	return float64(int64(value*100+0.5)) / 100
}

func signatureSimilarity(a, b [minHashCount]uint64) float64 {
	equal := 0
	for i := 0; i < minHashCount; i++ {
		if a[i] == b[i] {
			equal++
		}
	}
	return float64(equal) / float64(minHashCount)
}
