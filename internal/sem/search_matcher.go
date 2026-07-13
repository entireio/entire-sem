package sem

import "strings"

// searchTermMatcher is a compact ASCII case-insensitive Aho-Corasick matcher.
// Issue and source identifiers are overwhelmingly ASCII; uncommon non-ASCII
// terms use a separate Unicode-aware fallback.
type searchTermMatcher struct {
	nodes     []searchMatcherNode
	fallback  []searchFallbackTerm
	termCount int
}

type searchMatcherNode struct {
	next    map[byte]int
	failure int
	outputs []int
}

type searchFallbackTerm struct {
	index int
	term  string
}

func newSearchTermMatcher(terms []string) searchTermMatcher {
	matcher := searchTermMatcher{
		nodes:     []searchMatcherNode{{next: map[byte]int{}}},
		termCount: len(terms),
	}
	for index, term := range terms {
		if term == "" || !asciiString(term) {
			if term != "" {
				matcher.fallback = append(matcher.fallback, searchFallbackTerm{index: index, term: term})
			}
			continue
		}
		state := 0
		for offset := 0; offset < len(term); offset++ {
			character := lowerASCII(term[offset])
			next, ok := matcher.nodes[state].next[character]
			if !ok {
				next = len(matcher.nodes)
				matcher.nodes[state].next[character] = next
				matcher.nodes = append(matcher.nodes, searchMatcherNode{next: map[byte]int{}})
			}
			state = next
		}
		matcher.nodes[state].outputs = append(matcher.nodes[state].outputs, index)
	}
	matcher.buildFailures()
	return matcher
}

func (matcher *searchTermMatcher) buildFailures() {
	queue := make([]int, 0, len(matcher.nodes))
	for _, state := range matcher.nodes[0].next {
		queue = append(queue, state)
	}
	for len(queue) > 0 {
		state := queue[0]
		queue = queue[1:]
		for character, next := range matcher.nodes[state].next {
			queue = append(queue, next)
			failure := matcher.nodes[state].failure
			for failure != 0 {
				if target, ok := matcher.nodes[failure].next[character]; ok {
					failure = target
					break
				}
				failure = matcher.nodes[failure].failure
			}
			if failure == 0 {
				if target, ok := matcher.nodes[0].next[character]; ok && target != next {
					failure = target
				}
			}
			matcher.nodes[next].failure = failure
			matcher.nodes[next].outputs = append(matcher.nodes[next].outputs, matcher.nodes[failure].outputs...)
		}
	}
}

func (matcher searchTermMatcher) match(text string) []bool {
	found := make([]bool, matcher.termCount)
	remaining := matcher.termCount
	state := 0
	for offset := 0; offset < len(text) && remaining > 0; offset++ {
		character := text[offset]
		if character >= 128 {
			state = 0
			continue
		}
		character = lowerASCII(character)
		for state != 0 {
			if _, ok := matcher.nodes[state].next[character]; ok {
				break
			}
			state = matcher.nodes[state].failure
		}
		if next, ok := matcher.nodes[state].next[character]; ok {
			state = next
		}
		for _, index := range matcher.nodes[state].outputs {
			if !found[index] {
				found[index] = true
				remaining--
			}
		}
	}
	if len(matcher.fallback) > 0 {
		lower := strings.ToLower(text)
		for _, fallback := range matcher.fallback {
			if !found[fallback.index] && strings.Contains(lower, fallback.term) {
				found[fallback.index] = true
			}
		}
	}
	return found
}

func asciiString(value string) bool {
	for offset := 0; offset < len(value); offset++ {
		if value[offset] >= 128 {
			return false
		}
	}
	return true
}

func lowerASCII(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}
