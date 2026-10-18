// Package dbindex discovers local Beads workspaces without opening their stores.
package dbindex

import (
	"encoding/json"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const DefaultDirectory = "/opt/ra/firstmate/.beads"
const TTL = 15 * time.Minute

type Entry struct {
	Path    string    `json:"path"`
	Count   *int      `json:"issue_count,omitempty"`
	Prefix  string    `json:"prefix,omitempty"`
	Touched time.Time `json:"last_touched,omitempty"`
	Used    time.Time `json:"last_used,omitempty"`
}

func (e Entry) Name() string { return filepath.Base(filepath.Dir(e.Path)) }
func (e Entry) DisplayPath(home string) string {
	if rel, err := filepath.Rel(home, e.Path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Join("~", rel)
	}
	return e.Path
}

func Canonical(path string) string {
	path, err := filepath.Abs(path)
	if err != nil {
		log.Printf("database path: %v", err)
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

func excluded(name string) bool {
	switch name {
	case ".treehouse", ".no-mistakes", "vendor", "node_modules", ".cache", ".git":
		return true
	}
	return strings.HasPrefix(name, "actions-runner")
}

func excludedPath(path string) bool {
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if excluded(part) {
			return true
		}
	}
	return false
}

// Discover bounds traversal to maxDepth below each root, does not follow directory
// symlinks, and prunes stores themselves (Dolt internals can be very large).
func Discover(roots []string, maxDepth int) []Entry {
	found := map[string]Entry{}
	for _, root := range roots {
		root = Canonical(root)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if !os.IsNotExist(walkErr) {
					log.Printf("database discovery: %v", walkErr)
				}
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			storeLink := d.Name() == ".beads" && d.Type()&os.ModeSymlink != 0 && Exists(path)
			if !d.IsDir() && !storeLink {
				return nil
			}
			if excluded(d.Name()) {
				return filepath.SkipDir
			}
			rel, _ := filepath.Rel(root, path)
			depth := 0
			if rel != "." {
				depth = len(strings.Split(rel, string(filepath.Separator)))
			}
			if d.Name() == ".beads" {
				canonical := Canonical(path)
				if excludedPath(canonical) {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				entry := Entry{Path: canonical}
				for _, file := range []string{"last-touched", "metadata.json", "config.yaml"} {
					if info, err := os.Stat(filepath.Join(path, file)); err == nil && info.ModTime().After(entry.Touched) {
						entry.Touched = info.ModTime().UTC()
					}
				}
				found[canonical] = entry
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if depth >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			log.Printf("database discovery root: %v", err)
		}
	}
	entries := make([]Entry, 0, len(found))
	for _, entry := range found {
		entries = append(entries, entry)
	}
	return entries
}

func Sort(entries []Entry, active, explicit string) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if (a.Path == explicit) != (b.Path == explicit) {
			return a.Path == explicit
		}
		if (a.Path == active) != (b.Path == active) {
			return a.Path == active
		}
		if !a.Used.Equal(b.Used) {
			return a.Used.After(b.Used)
		}
		return a.Path < b.Path
	})
}

type Cache struct {
	Scanned time.Time `json:"scanned_at"`
	Entries []Entry   `json:"databases"`
}

func (c Cache) Fresh(now time.Time) bool {
	return !c.Scanned.IsZero() && !now.Before(c.Scanned) && now.Sub(c.Scanned) < TTL
}
func Read(path string) Cache {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("database index read: %v", err)
		}
		return Cache{}
	}
	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		log.Printf("database index parse: %v", err)
		return Cache{}
	}
	seen := map[string]bool{}
	entries := c.Entries[:0]
	for _, e := range c.Entries {
		e.Path = Canonical(e.Path)
		if e.Path != "" && !seen[e.Path] {
			seen[e.Path] = true
			entries = append(entries, e)
		}
	}
	c.Entries = entries
	return c
}
func Write(path string, c Cache) {
	data, err := json.MarshalIndent(c, "", "  ")
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0700)
	}
	if err == nil {
		err = os.WriteFile(path, data, 0600)
	}
	if err != nil {
		log.Printf("database index write: %v", err)
	}
}

func Exists(path string) bool { info, err := os.Stat(path); return err == nil && info.IsDir() }

// Select honors an explicit script target even if missing, then restores a
// surviving choice. Clean launches prefer the historical fleet store.
func Select(explicit, saved, fallback string, entries []Entry) string {
	if explicit != "" {
		return Canonical(explicit)
	}
	if saved != "" && Exists(saved) {
		return Canonical(saved)
	}
	if Exists(fallback) {
		return Canonical(fallback)
	}
	for _, e := range entries {
		if Exists(e.Path) {
			return Canonical(e.Path)
		}
	}
	return ""
}
