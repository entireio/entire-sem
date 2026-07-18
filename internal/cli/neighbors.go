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
	FormatVersion int                   `json:"format_version"`
	RepoRoot      string                `json:"repo_root"`
	Commit        string                `json:"commit,omitempty"`
	Tree          string                `json:"tree,omitempty"`
	Profile       string                `json:"profile"`
	Relation      string                `json:"relation"`
	Query         string                `json:"query"`
	File          string                `json:"file,omitempty"`
	Truncated     bool                  `json:"truncated"`
	Matches       []neighborFocus       `json:"matches"`
	Warnings      []sem.ProviderWarning `json:"warnings,omitempty"`
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
	snapshot, _, err := sem.LoadOrBuildProviderSnapshot(ctx, repo, opts.Version, sem.ProviderSnapshotOptions{
		NoNetwork:    true,
		Worktree:     flags.Worktree,
		IgnoreFiles:  flags.IgnoreFile,
		IncludeFiles: flags.IncludeFile,
		Profile:      profile,
	}, cacheDir, flags.DisableCache)
	if err != nil {
		return err
	}
	response := buildNeighborResponse(snapshot, flags)
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
	response := neighborResponse{
		FormatVersion: 1,
		RepoRoot:      snapshot.Header.RepoRoot,
		Commit:        snapshot.Header.Commit,
		Tree:          snapshot.Header.Tree,
		Profile:       snapshot.Header.Profile,
		Relation:      flags.Relation,
		Query:         flags.Symbol,
		File:          flags.File,
		Matches:       make([]neighborFocus, 0, len(focuses)),
		Warnings:      snapshot.Header.Warnings,
	}
	for _, focus := range focuses {
		entry := neighborFocus{
			Symbol:   endpointForSymbol(focus),
			Incoming: []neighborEdge{},
			Outgoing: []neighborEdge{},
		}
		for _, relation := range snapshot.Relations {
			if !neighborRelationMatches(flags.Relation, relation.Type) {
				continue
			}
			if relation.ToID == focus.ID && flags.Direction != "out" {
				if endpoint, ok := endpoints[relation.FromID]; ok {
					entry.Incoming = append(entry.Incoming, edgeForRelation("in", endpoint, relation))
				}
			}
			if relation.FromID == focus.ID && flags.Direction != "in" {
				if endpoint, ok := endpoints[relation.ToID]; ok {
					entry.Outgoing = append(entry.Outgoing, edgeForRelation("out", endpoint, relation))
				}
			}
		}
		sortNeighborEdges(entry.Incoming)
		sortNeighborEdges(entry.Outgoing)
		if len(entry.Incoming) > flags.Limit {
			entry.Incoming = entry.Incoming[:flags.Limit]
			response.Truncated = true
		}
		if len(entry.Outgoing) > flags.Limit {
			entry.Outgoing = entry.Outgoing[:flags.Limit]
			response.Truncated = true
		}
		if flags.Depth == 2 && flags.Direction == "both" {
			for _, incoming := range entry.Incoming {
				for _, outgoing := range entry.Outgoing {
					if len(entry.Paths) >= flags.Limit {
						response.Truncated = true
						break
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
	if len(response.Matches) == 0 {
		_, err := fmt.Fprintf(out, "No symbols matched %q. Add --file to disambiguate a known definition.\n", response.Query)
		return err
	}
	for index, match := range response.Matches {
		if index > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "Focus: %s\n", formatNeighborEndpoint(match.Symbol))
		writeNeighborEdgeList(out, "Callers", match.Incoming)
		writeNeighborEdgeList(out, "Callees", match.Outgoing)
		if len(match.Paths) > 0 {
			fmt.Fprintln(out, "Two-hop paths:")
			for _, path := range match.Paths {
				fmt.Fprintf(out, "- %s -> %s -> %s\n", endpointDisplayName(path.Caller), endpointDisplayName(path.Focus), endpointDisplayName(path.Callee))
			}
		}
	}
	if response.Truncated {
		fmt.Fprintln(out, "Results truncated; increase --limit for more neighbors or paths.")
	}
	return nil
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
