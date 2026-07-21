package sem

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Provider records (the streamed snapshot/symbols/edges NDJSON emitted by the
// `graph` commands) are deterministic for a given git tree, indexing mode, and
// set of options. Recomputing them on every call is expensive on large repos, so
// we cache the raw NDJSON bytes keyed on the HEAD tree hash plus everything else
// that changes the output. This mirrors the search-snapshot cache next door.
const providerRecordsCacheVersion = "provider-records-v1"

// cachedProviderRecords is the on-disk envelope for a cached record stream. The
// key alone is authoritative (sha256 over version+tree+mode+profile+options+
// path-file contents), but we re-validate the discriminating fields on read as
// defense-in-depth against a stale or hand-edited cache file.
type cachedProviderRecords struct {
	CacheVersion    string           `json:"cache_version"`
	ProviderVersion string           `json:"provider_version"`
	Tree            string           `json:"tree"`
	Mode            string           `json:"mode"`
	Profile         Profile          `json:"profile"`
	MaxParseBytes   int              `json:"max_parse_bytes"`
	Records         []byte           `json:"records"`
	Summary         *SnapshotSummary `json:"summary,omitempty"`
}

// providerRecordsKey derives the cache key for a record stream. It intentionally
// folds in the indexing mode (snapshot|symbols|edges) and the profile so the
// three modes and three profiles never collide, and it hashes the contents of
// any --ignore-file / --include-file inputs so an edit to those files misses the
// cache. OnlyFiles is included for completeness even though the record commands
// do not expose it. Callers must NOT use this for --worktree runs (the working
// tree can differ from HEAD) or for targeted --to/--from/--relation queries.
func providerRecordsKey(absRepo, providerVersion, tree, mode string, options ProviderSnapshotOptions) (string, error) {
	hash := sha256.New()
	writePart := func(value string) {
		_, _ = io.WriteString(hash, value)
		_, _ = io.WriteString(hash, "\x00")
	}
	writePart(providerRecordsCacheVersion)
	writePart(absRepo)
	writePart(providerVersion)
	writePart(tree)
	writePart(mode)
	writePart(string(options.Profile))
	writePart(fmt.Sprintf("%d", options.MaxParseBytes))
	onlyFiles := append([]string(nil), options.OnlyFiles...)
	sort.Strings(onlyFiles)
	writePart("only-files")
	for _, filePath := range onlyFiles {
		writePart(filepath.ToSlash(filepath.Clean(filePath)))
	}
	for groupIndex, group := range [][]string{options.IgnoreFiles, options.IncludeFiles} {
		writePart(fmt.Sprintf("path-group-%d", groupIndex))
		paths := append([]string(nil), group...)
		sort.Strings(paths)
		for _, path := range paths {
			resolved := path
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(absRepo, resolved)
			}
			writePart(filepath.Clean(resolved))
			content, err := os.ReadFile(resolved)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					writePart("missing")
					continue
				}
				return "", err
			}
			_, _ = hash.Write(content)
			writePart("")
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func providerRecordsPath(cacheDir, key string) string {
	return filepath.Join(cacheDir, "records", providerRecordsCacheVersion, key+".json.gz")
}

// LoadProviderRecords returns the cached NDJSON record stream for repo at the
// given tree/mode/options, or hit=false when there is no usable cache entry.
// The returned summary (when present) lets the caller reproduce the partial-parse
// warning without re-indexing. A missing/corrupt cache is a miss, not an error;
// only a key-derivation failure (an unreadable ignore/include file) errors.
func LoadProviderRecords(repo, providerVersion, tree, mode, cacheDir string, options ProviderSnapshotOptions) ([]byte, *SnapshotSummary, bool, error) {
	if cacheDir == "" || tree == "" || options.Worktree {
		return nil, nil, false, nil
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return nil, nil, false, err
	}
	key, err := providerRecordsKey(absRepo, providerVersion, tree, mode, options)
	if err != nil {
		return nil, nil, false, err
	}
	cache, err := readProviderRecords(providerRecordsPath(cacheDir, key))
	if err != nil || !validCachedProviderRecords(cache, providerVersion, tree, mode, options) {
		return nil, nil, false, nil
	}
	return cache.Records, cache.Summary, true, nil
}

// StoreProviderRecords persists a freshly built record stream. Persistence is
// best effort: retrieval correctness never depends on a writable cache dir, so
// callers may ignore the returned error.
func StoreProviderRecords(repo, providerVersion, tree, mode, cacheDir string, options ProviderSnapshotOptions, records []byte, summary *SnapshotSummary) error {
	if cacheDir == "" || tree == "" || options.Worktree {
		return nil
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return err
	}
	key, err := providerRecordsKey(absRepo, providerVersion, tree, mode, options)
	if err != nil {
		return err
	}
	cache := cachedProviderRecords{
		CacheVersion:    providerRecordsCacheVersion,
		ProviderVersion: providerVersion,
		Tree:            tree,
		Mode:            mode,
		Profile:         options.Profile,
		MaxParseBytes:   options.MaxParseBytes,
		Records:         records,
		Summary:         summary,
	}
	return writeProviderRecords(providerRecordsPath(cacheDir, key), cache)
}

func validCachedProviderRecords(cache cachedProviderRecords, providerVersion, tree, mode string, options ProviderSnapshotOptions) bool {
	return cache.CacheVersion == providerRecordsCacheVersion &&
		cache.ProviderVersion == providerVersion &&
		cache.Tree == tree &&
		cache.Mode == mode &&
		cache.Profile == options.Profile &&
		cache.MaxParseBytes == options.MaxParseBytes
}

func readProviderRecords(path string) (cachedProviderRecords, error) {
	file, err := os.Open(path)
	if err != nil {
		return cachedProviderRecords{}, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return cachedProviderRecords{}, err
	}
	defer reader.Close()
	var cache cachedProviderRecords
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&cache); err != nil {
		return cachedProviderRecords{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return cachedProviderRecords{}, errors.New("provider records cache has trailing data")
	}
	return cache, nil
}

func writeProviderRecords(path string, cache cachedProviderRecords) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".records-*.json.gz")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	writer := gzip.NewWriter(temporary)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(cache); err != nil {
		_ = writer.Close()
		_ = temporary.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}
