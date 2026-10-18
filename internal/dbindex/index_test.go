package dbindex

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func makeStore(t *testing.T, root, project string) string {
	t.Helper()
	path := filepath.Join(root, project, ".beads")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	return Canonical(path)
}
func TestDiscoveryExcludesWorktreePoolsAndDependencies(t *testing.T) {
	root := t.TempDir()
	want := makeStore(t, root, "git/wiseman")
	for _, p := range []string{".treehouse/pool/1/repo", ".no-mistakes/run", "git/repo/node_modules/pkg", "git/repo/vendor/pkg", ".cache/repo", "git/repo/.git/worktrees/foo", "actions-runner-2/_work/repo"} {
		makeStore(t, root, p)
	}
	makeStore(t, root, "one/two/three/four/five/six")
	entries := Discover([]string{root, filepath.Join(root, "git")}, 5)
	if len(entries) != 1 || entries[0].Path != want {
		t.Fatalf("discovered %+v, want only %s", entries, want)
	}
}
func TestIndexCacheTTLAndRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	count := 12
	c := Cache{Scanned: now, Entries: []Entry{{Path: makeStore(t, t.TempDir(), "repo"), Count: &count, Prefix: "fm", Used: now, Touched: now}}}
	path := filepath.Join(t.TempDir(), "nested", "index.json")
	Write(path, c)
	got := Read(path)
	if !got.Fresh(now.Add(TTL-time.Second)) || got.Fresh(now.Add(TTL)) || got.Fresh(now.Add(-time.Second)) {
		t.Fatal("incorrect freshness window")
	}
	if len(got.Entries) != 1 || *got.Entries[0].Count != 12 || got.Entries[0].Prefix != "fm" || !got.Entries[0].Used.Equal(now) {
		t.Fatalf("cache lost metadata: %+v", got)
	}
	if Read(filepath.Join(t.TempDir(), "missing")).Fresh(now) {
		t.Fatal("missing cache is fresh")
	}
}
func TestSelectionPrecedenceFallbackAndNoDatabases(t *testing.T) {
	root := t.TempDir()
	fallback := makeStore(t, root, "fleet")
	saved := makeStore(t, root, "saved")
	explicit := filepath.Join(root, "missing", ".beads")
	if got := Select(explicit, saved, fallback, nil); got != explicit {
		t.Fatalf("explicit missing target lost: %s", got)
	}
	if got := Select("", saved, fallback, nil); got != saved {
		t.Fatalf("saved choice lost: %s", got)
	}
	if got := Select("", explicit, fallback, nil); got != fallback {
		t.Fatalf("missing saved choice did not fall back: %s", got)
	}
	if got := Select("", "", fallback, []Entry{{Path: saved}}); got != fallback {
		t.Fatalf("clean launch not fleet: %s", got)
	}
	if got := Select("", "", explicit, []Entry{{Path: saved}}); got != saved {
		t.Fatalf("index fallback missing: %s", got)
	}
	if got := Select("", "", explicit, nil); got != "" {
		t.Fatalf("no databases selected %q", got)
	}
}
func TestSortPinnedActiveThenMRU(t *testing.T) {
	now := time.Now()
	entries := []Entry{{Path: "z", Used: now}, {Path: "active"}, {Path: "pin"}, {Path: "a"}}
	Sort(entries, "active", "pin")
	for i, want := range []string{"pin", "active", "z", "a"} {
		if entries[i].Path != want {
			t.Fatalf("sort: %+v", entries)
		}
	}
}

func TestDiscoveryDeduplicatesStoreSymlinksWithoutFollowingPools(t *testing.T) {
	root := t.TempDir()
	real := makeStore(t, root, "git/real")
	alias := filepath.Join(root, "git", "alias")
	if err := os.MkdirAll(alias, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(alias, ".beads")); err != nil {
		t.Fatal(err)
	}
	hidden := makeStore(t, root, ".treehouse/pool/1/repo")
	if err := os.Symlink(hidden, filepath.Join(root, ".beads")); err != nil {
		t.Fatal(err)
	}
	entries := Discover([]string{root}, 5)
	if len(entries) != 1 || entries[0].Path != real {
		t.Fatalf("canonical discovery: %+v", entries)
	}
}
