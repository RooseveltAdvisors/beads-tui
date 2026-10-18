package bd

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestDirectoryClientsKeepReadsAndWritesIsolated(t *testing.T) {
	t.Setenv("BEADS_DIR", "ambient")
	root := t.TempDir()
	script := filepath.Join(root, "bd")
	source := `#!/bin/sh
case "$*" in
 *comment*) cat > "$BEADS_DIR/comment.txt" ;;
 *) printf '[{"id":"same-id","title":"%s"}]' "$BEADS_DIR" ;;
esac
`
	if err := os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	client := New()
	client.lookPath = func(string) (string, error) { return script, nil }
	client.Actor = "test"
	var wg sync.WaitGroup
	for _, name := range []string{"a", "b"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		scoped := client.ForDirectory(dir)
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, err := scoped.List(context.Background(), ViewReady)
			if err != nil || len(rows) != 1 || rows[0].Title != dir {
				t.Errorf("directory read: %+v %v", rows, err)
				return
			}
			if err := scoped.AddComment(context.Background(), "same-id", name); err != nil {
				t.Error(err)
				return
			}
			data, err := os.ReadFile(filepath.Join(dir, "comment.txt"))
			if err != nil || string(data) != name {
				t.Errorf("directory write: %q %v", data, err)
			}
		}()
	}
	wg.Wait()
	if os.Getenv("BEADS_DIR") != "ambient" {
		t.Fatal("process environment changed")
	}
}

func TestPrefixUsesConfiguredValueForHyphenatedIDs(t *testing.T) {
	client := New()
	client.lookPath = func(string) (string, error) { return "bd", nil }
	client.run = func(_ context.Context, _ string, args ...string) (string, string, error) {
		want := []string{"config", "get", "issue_prefix", "--json"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("prefix args: %v", args)
		}
		return `{"key":"issue_prefix","value":"my-project"}`, "", nil
	}
	prefix, err := client.Prefix(context.Background())
	if err != nil || prefix != "my-project" {
		t.Fatalf("prefix=%q err=%v", prefix, err)
	}
}
