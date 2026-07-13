package sem

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/entireio/entire-sem/internal/gitutil"
)

const searchSnapshotCacheVersion = "search-snapshot-v1"

type cachedSearchSnapshot struct {
	CacheVersion    string           `json:"cache_version"`
	ProviderVersion string           `json:"provider_version"`
	Tree            string           `json:"tree"`
	Profile         Profile          `json:"profile"`
	MaxParseBytes   int              `json:"max_parse_bytes"`
	Snapshot        ProviderSnapshot `json:"snapshot"`
}

func loadOrBuildSearchSnapshot(
	ctx context.Context,
	repo, providerVersion string,
	options ProviderSnapshotOptions,
	cacheDir string,
	disableCache bool,
) (ProviderSnapshot, bool, error) {
	if disableCache || cacheDir == "" || options.Worktree {
		snapshot, err := BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, options)
		return snapshot, false, err
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	tree, err := gitutil.RevParse(ctx, absRepo, "HEAD^{tree}")
	if err != nil || tree == "" {
		snapshot, buildErr := BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, options)
		return snapshot, false, buildErr
	}
	key, err := searchSnapshotKey(absRepo, providerVersion, tree, options)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	path := filepath.Join(cacheDir, "search", searchSnapshotCacheVersion, key+".json.gz")
	if cached, err := readSearchSnapshot(path); err == nil && validCachedSearchSnapshot(cached, providerVersion, tree, options) {
		return cached.Snapshot, true, nil
	}
	snapshot, err := BuildProviderSnapshotWithOptions(ctx, repo, providerVersion, options)
	if err != nil {
		return ProviderSnapshot{}, false, err
	}
	cache := cachedSearchSnapshot{
		CacheVersion:    searchSnapshotCacheVersion,
		ProviderVersion: providerVersion,
		Tree:            tree,
		Profile:         options.Profile,
		MaxParseBytes:   options.MaxParseBytes,
		Snapshot:        snapshot,
	}
	// Cache persistence is best effort. Retrieval correctness never depends on
	// a writable cache directory.
	_ = writeSearchSnapshot(path, cache)
	return snapshot, false, nil
}

func searchSnapshotKey(absRepo, providerVersion, tree string, options ProviderSnapshotOptions) (string, error) {
	hash := sha256.New()
	writePart := func(value string) {
		_, _ = io.WriteString(hash, value)
		_, _ = io.WriteString(hash, "\x00")
	}
	writePart(searchSnapshotCacheVersion)
	writePart(absRepo)
	writePart(providerVersion)
	writePart(tree)
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

func validCachedSearchSnapshot(cache cachedSearchSnapshot, providerVersion, tree string, options ProviderSnapshotOptions) bool {
	return cache.CacheVersion == searchSnapshotCacheVersion &&
		cache.ProviderVersion == providerVersion &&
		cache.Tree == tree &&
		cache.Profile == options.Profile &&
		cache.MaxParseBytes == options.MaxParseBytes &&
		cache.Snapshot.Header.Tree == tree &&
		cache.Snapshot.Header.Provider == ProviderName &&
		cache.Snapshot.Header.Profile == string(options.Profile)
}

func readSearchSnapshot(path string) (cachedSearchSnapshot, error) {
	file, err := os.Open(path)
	if err != nil {
		return cachedSearchSnapshot{}, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return cachedSearchSnapshot{}, err
	}
	defer reader.Close()
	var cache cachedSearchSnapshot
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&cache); err != nil {
		return cachedSearchSnapshot{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return cachedSearchSnapshot{}, errors.New("search snapshot cache has trailing data")
	}
	return cache, nil
}

func writeSearchSnapshot(path string, cache cachedSearchSnapshot) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".snapshot-*.json.gz")
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
