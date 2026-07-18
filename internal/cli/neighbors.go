package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/entireio/entire-graph/internal/sem"
)

const defaultNeighborLimit = 20

type neighborFlags struct {
	Repo         string
	Symbol       string
	File         string
	Format       string
	Profile      string
	Relation     string
	Direction    string
	Depth        int
	Limit        int
	Worktree     bool
	IgnoreFile   []string
	IncludeFile  []string
	CacheDir     string
	DisableCache bool
	InternalOnly bool
	ExcludeTests bool
}

type neighborEndpoint struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name,omitempty"`
	Kind          string `json:"kind,omitempty"`
	FilePath      string `json:"file_path,omitempty"`
	StartLine     int    `json:"start_line,omitempty"`
	External      bool   `json:"external,omitempty"`
}

type neighborEdge struct {
	Direction  string           `json:"direction"`
	Relation   string           `json:"relation"`
	Endpoint   neighborEndpoint `json:"endpoint"`
	Confidence float64          `json:"confidence"`
	Resolution string           `json:"resolution,omitempty"`
	Reason     string           `json:"reason,omitempty"`
	Evidence   []sem.Evidence   `json:"evidence,omitempty"`
}

type neighborPath struct {
	Caller neighborEndpoint `json:"caller"`
	Focus  neighborEndpoint `json:"focus"`
	Callee neighborEndpoint `json:"callee"`
}

type neighborFocus struct {
	Symbol   neighborEndpoint `json:"symbol"`
	Incoming []neighborEdge   `json:"incoming"`
	Outgoing []neighborEdge   `json:"outgoing"`
	Paths    []neighborPath   `json:"paths,omitempty"`
}

type neighborResponse struct {
	FormatVersion         int                    `json:"format_version"`
	RepoRoot              string                 `json:"repo_root"`
	Commit                string                 `json:"commit,omitempty"`
	Tree                  string                 `json:"tree,omitempty"`
	Profile               string                 `json:"profile"`
	Relation              string                 `json:"relation"`
	Query                 string                 `json:"query"`
	File                  string                 `json:"file,omitempty"`
	IndexCacheHit         bool                   `json:"index_cache_hit"`
	IndexLatencyMS        int64                  `json:"index_latency_ms"`
	QueryLatencyMS        int64                  `json:"query_latency_ms"`
	TotalLatencyMS        int64                  `json:"total_latency_ms"`
	Truncated             bool                   `json:"truncated"`
	FocusMatchesTotal     int                    `json:"focus_matches_total"`
	FocusMatchesTruncated bool                   `json:"focus_matches_truncated"`
	Matches               []neighborFocus        `json:"matches"`
	Warnings              []sem.ProviderWarning  `json:"warnings,omitempty"`
	PartialFailures       []sem.PartialFailure   `json:"partial_failures"`
	Stats                 sem.ProviderStats      `json:"stats"`
	Completeness          sem.CompletenessReport `json:"completeness"`

	// endpointTruncated distinguishes a bounded neighbor list from the JSON-only
	// explicit path expansion. Agent output can express the full Cartesian path
	// family compactly, so path expansion alone is not a truncation for agents.
	endpointTruncated bool
}

func runNeighbors(ctx context.Context, opts Options, args []string) error {
	flags, err := parseNeighborFlags(args)
	if err != nil {
		return err
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	profile, err := parseProfile(flags.Profile)
	if err != nil {
		return err
	}
	cacheDir := flags.CacheDir
	if cacheDir == "" {
		cacheDir = opts.Env.PluginDataDir
	}
	totalStarted := time.Now()
	indexStarted := totalStarted
	snapshot, cacheHit, err := sem.LoadOrBuildProviderSnapshot(ctx, repo, opts.Version, sem.ProviderSnapshotOptions{
		NoNetwork:    true,
		Worktree:     flags.Worktree,
		IgnoreFiles:  flags.IgnoreFile,
		IncludeFiles: flags.IncludeFile,
		Profile:      profile,
	}, cacheDir, flags.DisableCache)
	if err != nil {
		return err
	}
	indexLatency := time.Since(indexStarted)
	queryStarted := time.Now()
	response := buildNeighborResponse(snapshot, flags)
	queryLatency := time.Since(queryStarted)
	response.IndexCacheHit = cacheHit
	response.IndexLatencyMS = indexLatency.Milliseconds()
	response.QueryLatencyMS = queryLatency.Milliseconds()
	response.TotalLatencyMS = time.Since(totalStarted).Milliseconds()
	switch flags.Format {
	case "json":
		encoder := json.NewEncoder(opts.Stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(response)
	case "agent", "text":
		return writeAgentNeighbors(opts.Stdout, response)
	default:
		return fmt.Errorf("neighbors --format must be json, text, or agent, got %q", flags.Format)
	}
}

func parseNeighborFlags(args []string) (neighborFlags, error) {
	flags := neighborFlags{
		Format: "json", Profile: "full", Relation: "CALLS", Direction: "both",
		Depth: 1, Limit: defaultNeighborLimit, Worktree: true,
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		value := func() (string, error) {
			index++
			if index >= len(args) {
				return "", fmt.Errorf("%s requires a value", arg)
			}
			return args[index], nil
		}
		switch arg {
		case "--repo":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.Repo = item
		case "--symbol":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.Symbol = item
		case "--file":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.File = item
		case "--format":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.Format = item
		case "--profile":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.Profile = item
		case "--relation":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.Relation = strings.ToUpper(item)
		case "--direction":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.Direction = item
		case "--depth":
			parsed, next, parseErr := searchPositiveIntFlag(args, index)
			if parseErr != nil {
				return flags, parseErr
			}
			flags.Depth, index = parsed, next
		case "--limit":
			parsed, next, parseErr := searchPositiveIntFlag(args, index)
			if parseErr != nil {
				return flags, parseErr
			}
			flags.Limit, index = parsed, next
		case "--head":
			flags.Worktree = false
		case "--worktree":
			flags.Worktree = true
		case "--ignore-file":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.IgnoreFile = append(flags.IgnoreFile, item)
		case "--include-file":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.IncludeFile = append(flags.IncludeFile, item)
		case "--cache-dir":
			item, valueErr := value()
			if valueErr != nil {
				return flags, valueErr
			}
			flags.CacheDir = item
		case "--no-cache":
			flags.DisableCache = true
		case "--internal-only":
			flags.InternalOnly = true
		case "--exclude-tests":
			flags.ExcludeTests = true
		default:
			return flags, fmt.Errorf("neighbors received unexpected argument %q", arg)
		}
	}
	if strings.TrimSpace(flags.Symbol) == "" {
		return flags, errors.New("neighbors requires --symbol")
	}
	if flags.Depth != 1 && flags.Depth != 2 {
		return flags, errors.New("neighbors --depth must be 1 or 2")
	}
	if flags.Direction != "both" && flags.Direction != "in" && flags.Direction != "out" {
		return flags, errors.New("neighbors --direction must be both, in, or out")
	}
	if flags.Relation == "" {
		return flags, errors.New("neighbors --relation cannot be empty")
	}
	return flags, nil
}

func buildNeighborResponse(snapshot sem.ProviderSnapshot, flags neighborFlags) neighborResponse {
	endpoints := make(map[string]neighborEndpoint, len(snapshot.Symbols)+len(snapshot.Externals))
	for _, file := range snapshot.Files {
		endpoints[file.ID] = endpointForFile(file)
	}
	for _, symbol := range snapshot.Symbols {
		endpoints[symbol.ID] = endpointForSymbol(symbol)
	}
	for _, external := range snapshot.Externals {
		endpoints[external.ID] = endpointForExternal(external)
	}
	focuses := make([]sem.SymbolRecord, 0)
	query := strings.TrimSpace(flags.Symbol)
	for _, symbol := range snapshot.Symbols {
		if !strings.EqualFold(symbol.Name, query) && !strings.EqualFold(symbol.QualifiedName, query) {
			continue
		}
		if flags.File != "" && !strings.EqualFold(symbol.FilePath, flags.File) {
			continue
		}
		focuses = append(focuses, symbol)
	}
	sort.Slice(focuses, func(left, right int) bool {
		if focuses[left].FilePath != focuses[right].FilePath {
			return focuses[left].FilePath < focuses[right].FilePath
		}
		if focuses[left].StartLine != focuses[right].StartLine {
			return focuses[left].StartLine < focuses[right].StartLine
		}
		return focuses[left].ID < focuses[right].ID
	})
	focusMatchesTotal := len(focuses)
	focusMatchesTruncated := focusMatchesTotal > flags.Limit
	if focusMatchesTruncated {
		focuses = focuses[:flags.Limit]
	}
	partialFailures := snapshot.Header.PartialFailures
	if partialFailures == nil {
		partialFailures = []sem.PartialFailure{}
	}
	response := neighborResponse{
		FormatVersion:         1,
		RepoRoot:              snapshot.Header.RepoRoot,
		Commit:                snapshot.Header.Commit,
		Tree:                  snapshot.Header.Tree,
		Profile:               snapshot.Header.Profile,
		Relation:              flags.Relation,
		Query:                 flags.Symbol,
		File:                  flags.File,
		Truncated:             focusMatchesTruncated,
		FocusMatchesTotal:     focusMatchesTotal,
		FocusMatchesTruncated: focusMatchesTruncated,
		Matches:               make([]neighborFocus, 0, len(focuses)),
		Warnings:              snapshot.Header.Warnings,
		PartialFailures:       partialFailures,
		Stats:                 snapshot.Header.Stats,
		Completeness:          snapshot.Header.Completeness,
	}

	// Index the requested adjacency once. Ambiguous symbols can have many focus
	// definitions, and rescanning every provider relation for every definition
	// makes a bounded --limit query do unbounded repeated work.
	focusIDs := make(map[string]bool, len(focuses))
	for _, focus := range focuses {
		focusIDs[focus.ID] = true
	}
	incomingByFocus := make(map[string][]neighborEdge, len(focuses))
	outgoingByFocus := make(map[string][]neighborEdge, len(focuses))
	for _, relation := range snapshot.Relations {
		if !neighborRelationMatches(flags.Relation, relation.Type) {
			continue
		}
		if flags.Direction != "out" && focusIDs[relation.ToID] {
			if endpoint, ok := endpoints[relation.FromID]; ok && neighborEndpointAllowed(endpoint, flags) {
				incomingByFocus[relation.ToID] = append(
					incomingByFocus[relation.ToID], edgeForRelation("in", endpoint, relation),
				)
			}
		}
		if flags.Direction != "in" && focusIDs[relation.FromID] {
			if endpoint, ok := endpoints[relation.ToID]; ok && neighborEndpointAllowed(endpoint, flags) {
				outgoingByFocus[relation.FromID] = append(
					outgoingByFocus[relation.FromID], edgeForRelation("out", endpoint, relation),
				)
			}
		}
	}
	for _, focus := range focuses {
		entry := neighborFocus{
			Symbol:   endpointForSymbol(focus),
			Incoming: incomingByFocus[focus.ID],
			Outgoing: outgoingByFocus[focus.ID],
		}
		if entry.Incoming == nil {
			entry.Incoming = []neighborEdge{}
		}
		if entry.Outgoing == nil {
			entry.Outgoing = []neighborEdge{}
		}
		sortNeighborEdges(entry.Incoming)
		sortNeighborEdges(entry.Outgoing)
		if len(entry.Incoming) > flags.Limit {
			entry.Incoming = entry.Incoming[:flags.Limit]
			response.Truncated = true
			response.endpointTruncated = true
		}
		if len(entry.Outgoing) > flags.Limit {
			entry.Outgoing = entry.Outgoing[:flags.Limit]
			response.Truncated = true
			response.endpointTruncated = true
		}
		if flags.Depth == 2 && flags.Direction == "both" {
		paths:
			for _, incoming := range entry.Incoming {
				for _, outgoing := range entry.Outgoing {
					if len(entry.Paths) >= flags.Limit {
						response.Truncated = true
						break paths
					}
					entry.Paths = append(entry.Paths, neighborPath{
						Caller: incoming.Endpoint,
						Focus:  entry.Symbol,
						Callee: outgoing.Endpoint,
					})
				}
			}
		}
		response.Matches = append(response.Matches, entry)
	}
	return response
}

func endpointForSymbol(symbol sem.SymbolRecord) neighborEndpoint {
	return neighborEndpoint{
		ID: symbol.ID, Name: symbol.Name, QualifiedName: symbol.QualifiedName,
		Kind: symbol.Kind, FilePath: symbol.FilePath, StartLine: symbol.StartLine,
	}
}

func endpointForFile(file sem.FileRecord) neighborEndpoint {
	return neighborEndpoint{
		ID: file.ID, Name: filepath.Base(file.Path), Kind: "file", FilePath: file.Path,
	}
}

func neighborEndpointAllowed(endpoint neighborEndpoint, flags neighborFlags) bool {
	if flags.InternalOnly && endpoint.External {
		return false
	}
	return !flags.ExcludeTests || !isConventionalTestPath(endpoint.FilePath)
}

func isConventionalTestPath(path string) bool {
	clean := strings.Trim(filepath.ToSlash(filepath.Clean(path)), "/")
	if clean == "" || clean == "." {
		return false
	}
	parts := strings.Split(clean, "/")
	for _, part := range parts[:len(parts)-1] {
		switch strings.ToLower(part) {
		case "test", "tests", "__tests__", "spec", "specs", "testdata":
			return true
		}
	}

	base := parts[len(parts)-1]
	lowerBase := strings.ToLower(base)
	if strings.Contains(lowerBase, ".test.") || strings.Contains(lowerBase, ".spec.") {
		return true
	}
	if strings.HasSuffix(lowerBase, "_test.go") ||
		strings.HasSuffix(lowerBase, "_test.py") ||
		strings.HasPrefix(lowerBase, "test_") && strings.HasSuffix(lowerBase, ".py") ||
		strings.HasSuffix(lowerBase, "_spec.rb") ||
		lowerBase == "test.rs" || lowerBase == "tests.rs" {
		return true
	}

	extension := filepath.Ext(base)
	stem := strings.TrimSuffix(base, extension)
	return strings.HasSuffix(stem, "Test") ||
		strings.HasSuffix(stem, "Tests") ||
		strings.HasSuffix(stem, "TestCase")
}

func neighborRelationMatches(requested, actual string) bool {
	// Constructors are callable dependencies. The provider schema keeps
	// CONSTRUCTS distinct, while the focused call-neighborhood view includes
	// them so "callees" does not silently omit direct constructor invocations.
	return actual == requested || (requested == "CALLS" && actual == "CONSTRUCTS")
}

func endpointForExternal(external sem.ExternalRecord) neighborEndpoint {
	name := external.Value
	if index := strings.LastIndexAny(name, ".:/#"); index >= 0 && index+1 < len(name) {
		name = name[index+1:]
	}
	return neighborEndpoint{
		ID: external.ID, Name: name, QualifiedName: external.Value, Kind: external.Kind,
		FilePath: external.FilePath, StartLine: external.StartLine, External: true,
	}
}

func edgeForRelation(direction string, endpoint neighborEndpoint, relation sem.RelationRecord) neighborEdge {
	return neighborEdge{
		Direction: direction, Relation: relation.Type, Endpoint: endpoint,
		Confidence: relation.Confidence, Resolution: relation.Resolution,
		Reason: relation.Reason, Evidence: relation.Evidence,
	}
}

func sortNeighborEdges(edges []neighborEdge) {
	sort.Slice(edges, func(left, right int) bool {
		leftName := edges[left].Endpoint.QualifiedName
		if leftName == "" {
			leftName = edges[left].Endpoint.Name
		}
		rightName := edges[right].Endpoint.QualifiedName
		if rightName == "" {
			rightName = edges[right].Endpoint.Name
		}
		if leftName != rightName {
			return leftName < rightName
		}
		if edges[left].Endpoint.FilePath != edges[right].Endpoint.FilePath {
			return edges[left].Endpoint.FilePath < edges[right].Endpoint.FilePath
		}
		return edges[left].Endpoint.StartLine < edges[right].Endpoint.StartLine
	})
}

func writeAgentNeighbors(out io.Writer, response neighborResponse) error {
	cacheState := "miss"
	if response.IndexCacheHit {
		cacheState = "hit"
	}
	fmt.Fprintf(out, "Index: cache-%s (%dms) | Query: %dms | Total: %dms\n",
		cacheState, response.IndexLatencyMS, response.QueryLatencyMS, response.TotalLatencyMS,
	)
	writeAgentNeighborCompleteness(out, response)
	if len(response.Matches) == 0 {
		_, err := fmt.Fprintf(out, "No symbols matched %q. Add --file to disambiguate a known definition.\n", response.Query)
		return err
	}
	if response.FocusMatchesTruncated {
		fmt.Fprintf(out,
			"Focus matches truncated: showing the first %d of %d in file/line order; use --file to select a definition.\n",
			len(response.Matches), response.FocusMatchesTotal,
		)
	}
	for index, match := range response.Matches {
		if index > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "Focus: %s\n", formatNeighborEndpoint(match.Symbol))
		writeNeighborEdgeList(out, "Callers", match.Incoming)
		writeNeighborEdgeList(out, "Callees", match.Outgoing)
		if len(match.Paths) > 0 {
			writeNeighborPathFamily(out, match)
		}
	}
	if response.endpointTruncated {
		fmt.Fprintln(out, "Neighbor lists truncated; increase --limit for more callers or callees.")
	}
	return nil
}

func writeAgentNeighborCompleteness(out io.Writer, response neighborResponse) {
	if len(response.PartialFailures) == 0 &&
		(response.Stats.CompletenessLevel == "" || response.Stats.CompletenessLevel == "ok") {
		return
	}
	level := response.Stats.CompletenessLevel
	if level == "" {
		level = "degraded"
	}
	if response.Stats.Files > 0 {
		fmt.Fprintf(out, "Completeness: %s (%d/%d files parsed; %d partial failure%s)\n",
			level, response.Stats.ParsedFiles, response.Stats.Files,
			len(response.PartialFailures), pluralSuffix(len(response.PartialFailures)),
		)
	} else {
		fmt.Fprintf(out, "Completeness: %s (%d partial failure%s)\n",
			level, len(response.PartialFailures), pluralSuffix(len(response.PartialFailures)),
		)
	}
	const maxAgentFailures = 3
	visible := len(response.PartialFailures)
	if visible > maxAgentFailures {
		visible = maxAgentFailures
	}
	for _, failure := range response.PartialFailures[:visible] {
		if failure.FilePath == "" {
			fmt.Fprintf(out, "- %s\n", failure.Code)
			continue
		}
		fmt.Fprintf(out, "- %s: %s\n", failure.Code, failure.FilePath)
	}
	if omitted := len(response.PartialFailures) - visible; omitted > 0 {
		fmt.Fprintf(out, "- ... %d more partial failure%s in JSON output\n", omitted, pluralSuffix(omitted))
	}
}

func writeNeighborPathFamily(out io.Writer, match neighborFocus) {
	callers := make([]neighborEndpoint, 0, len(match.Incoming))
	for _, edge := range match.Incoming {
		callers = append(callers, edge.Endpoint)
	}
	callees := make([]neighborEndpoint, 0, len(match.Outgoing))
	for _, edge := range match.Outgoing {
		callees = append(callees, edge.Endpoint)
	}
	pathCount := len(callers) * len(callees)
	fmt.Fprintf(out, "Two-hop path family (%d caller%s × 1 focus × %d callee%s = %d path%s):\n",
		len(callers), pluralSuffix(len(callers)), len(callees), pluralSuffix(len(callees)), pathCount, pluralSuffix(pathCount))
	fmt.Fprintf(out, "- %s -> %s -> %s\n",
		formatNeighborEndpointFamily(callers), endpointDisplayName(match.Symbol), formatNeighborEndpointFamily(callees))
}

func formatNeighborEndpointFamily(endpoints []neighborEndpoint) string {
	if len(endpoints) == 1 {
		return endpointDisplayName(endpoints[0])
	}
	items := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		items = append(items, formatNeighborEndpoint(endpoint))
	}
	return "{" + strings.Join(items, "; ") + "}"
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func writeNeighborEdgeList(out io.Writer, label string, edges []neighborEdge) {
	fmt.Fprintf(out, "%s:\n", label)
	if len(edges) == 0 {
		fmt.Fprintln(out, "- none")
		return
	}
	for _, edge := range edges {
		fmt.Fprintf(out, "- %s", formatNeighborEndpoint(edge.Endpoint))
		annotations := make([]string, 0, 3)
		if edge.Endpoint.Kind == "file" {
			annotations = append(annotations, "file-level")
		}
		if edge.Relation != "CALLS" {
			annotations = append(annotations, edge.Relation)
		}
		if edge.Resolution != "" {
			annotations = append(annotations, edge.Resolution)
		}
		if len(annotations) > 0 {
			fmt.Fprintf(out, " [%s]", strings.Join(annotations, ", "))
		}
		fmt.Fprintln(out)
	}
}

func formatNeighborEndpoint(endpoint neighborEndpoint) string {
	name := endpointDisplayName(endpoint)
	if endpoint.FilePath == "" {
		return name
	}
	if endpoint.StartLine > 0 {
		return fmt.Sprintf("%s (%s:%d)", name, endpoint.FilePath, endpoint.StartLine)
	}
	return fmt.Sprintf("%s (%s)", name, endpoint.FilePath)
}

func endpointDisplayName(endpoint neighborEndpoint) string {
	if endpoint.QualifiedName != "" {
		return endpoint.QualifiedName
	}
	return endpoint.Name
}
