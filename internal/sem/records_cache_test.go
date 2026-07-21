package sem

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestProviderRecordsCacheHitAndMiss(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	cacheDir := t.TempDir()
	const (
		version = "test-v1"
		tree    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		mode    = "snapshot"
	)
	opts := ProviderSnapshotOptions{Profile: ProfileFull}
	records := []byte("{\"record_type\":\"header\"}\n{\"record_type\":\"symbol\"}\n")
	summary := &SnapshotSummary{RecordType: "summary", Stats: ProviderStats{Files: 3}}

	if err := StoreProviderRecords(repo, version, tree, mode, cacheDir, opts, records, summary); err != nil {
		t.Fatalf("store: %v", err)
	}

	got, gotSummary, hit, err := LoadProviderRecords(repo, version, tree, mode, cacheDir, opts)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !hit {
		t.Fatal("expected cache hit after store")
	}
	if !bytes.Equal(got, records) {
		t.Fatalf("records mismatch: got %q want %q", got, records)
	}
	if gotSummary == nil || gotSummary.Stats.Files != 3 {
		t.Fatalf("summary not round-tripped: %#v", gotSummary)
	}

	// Each discriminator must independently invalidate the entry.
	cases := []struct {
		name    string
		version string
		tree    string
		mode    string
		opts    ProviderSnapshotOptions
	}{
		{"different mode", version, tree, "symbols", opts},
		{"different tree", version, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", mode, opts},
		{"different profile", version, tree, mode, ProviderSnapshotOptions{Profile: ProfileFast}},
		{"different version", "other", tree, mode, opts},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, hit, err := LoadProviderRecords(repo, tc.version, tc.tree, tc.mode, cacheDir, tc.opts)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if hit {
				t.Fatal("expected cache miss")
			}
		})
	}
}

func TestProviderRecordsCacheWorktreeBypassed(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	cacheDir := t.TempDir()
	opts := ProviderSnapshotOptions{Profile: ProfileFull, Worktree: true}
	records := []byte("x")

	// Store is a no-op under --worktree, and Load never reports a hit.
	if err := StoreProviderRecords(repo, "v", "tree", "snapshot", cacheDir, opts, records, nil); err != nil {
		t.Fatalf("store: %v", err)
	}
	if _, _, hit, err := LoadProviderRecords(repo, "v", "tree", "snapshot", cacheDir, opts); err != nil || hit {
		t.Fatalf("worktree load: hit=%v err=%v", hit, err)
	}
}

func TestProviderRecordsCacheKeyIncludesIgnoreFileContent(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	ignore := filepath.Join(repo, "ignore.txt")
	if err := os.WriteFile(ignore, []byte("first"), 0o600); err != nil {
		t.Fatalf("write ignore: %v", err)
	}
	opts := ProviderSnapshotOptions{Profile: ProfileFull, IgnoreFiles: []string{ignore}}

	before, err := providerRecordsKey(repo, "v", "tree", "snapshot", opts)
	if err != nil {
		t.Fatalf("key before: %v", err)
	}
	if err := os.WriteFile(ignore, []byte("second"), 0o600); err != nil {
		t.Fatalf("rewrite ignore: %v", err)
	}
	after, err := providerRecordsKey(repo, "v", "tree", "snapshot", opts)
	if err != nil {
		t.Fatalf("key after: %v", err)
	}
	if before == after {
		t.Fatal("expected key to change when ignore-file content changes")
	}
}
