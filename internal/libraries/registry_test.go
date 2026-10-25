package libraries

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"calibruh/internal/config"
	"calibruh/internal/library"
	"calibruh/internal/testlib"
)

func names(libs []*library.Library) []string {
	var out []string
	for _, l := range libs {
		out = append(out, l.Name)
	}
	return out
}

func newRegistry(t *testing.T, static []config.Library, dir string) *Registry {
	t.Helper()
	r, err := New(static, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}

func TestDiscovery(t *testing.T) {
	dir := t.TempDir()
	testlib.NewAt(t, filepath.Join(dir, "comics"))
	testlib.NewAt(t, filepath.Join(dir, "Books"))
	testlib.NewAt(t, filepath.Join(dir, ".hidden"))
	os.Mkdir(filepath.Join(dir, "not-a-library"), 0o755)
	os.WriteFile(filepath.Join(dir, "stray.txt"), nil, 0o644)
	broken := filepath.Join(dir, "broken")
	os.Mkdir(broken, 0o755)
	os.WriteFile(filepath.Join(broken, "metadata.db"), []byte("not sqlite"), 0o644)

	r := newRegistry(t, nil, dir)
	if got := names(r.List()); len(got) != 2 || got[0] != "Books" || got[1] != "comics" {
		t.Fatalf("libraries = %q, want [Books comics]", got)
	}
	if r.Get("books") == nil || r.Get("hidden") != nil {
		t.Error("lookup by slug is wrong")
	}
}

func TestRefreshAddsAndRemoves(t *testing.T) {
	dir := t.TempDir()
	testlib.NewAt(t, filepath.Join(dir, "Books"))
	r := newRegistry(t, nil, dir)
	books := r.Get("books")

	testlib.NewAt(t, filepath.Join(dir, "Manga"))
	if err := r.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := names(r.List()); len(got) != 2 || got[1] != "Manga" {
		t.Fatalf("after add: %q", got)
	}
	if r.Get("books") != books {
		t.Error("unchanged library was reopened")
	}

	os.RemoveAll(filepath.Join(dir, "Books"))
	if err := r.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := names(r.List()); len(got) != 1 || got[0] != "Manga" {
		t.Fatalf("after remove: %q", got)
	}
	if _, _, err := books.Books(context.Background(), library.Query{}); err == nil {
		t.Error("removed library was not closed")
	}
}

func TestStaticLibrariesComeFirstAndWinSlugs(t *testing.T) {
	dir := t.TempDir()
	testlib.NewAt(t, filepath.Join(dir, "books"))
	testlib.NewAt(t, filepath.Join(dir, "Archive"))
	elsewhere := testlib.New(t)
	inside := filepath.Join(dir, "Archive")

	r := newRegistry(t, []config.Library{
		{Name: "Books", Path: elsewhere},
		{Name: "Old stuff", Path: inside},
	}, dir)
	got := names(r.List())
	// "books" clashes with the configured "Books"; "Archive" is the same
	// folder as the configured "Old stuff" and is not listed twice.
	if len(got) != 2 || got[0] != "Books" || got[1] != "Old stuff" {
		t.Fatalf("libraries = %q", got)
	}
	if r.Get("books").Root != elsewhere {
		t.Error("discovered library took a configured library's slug")
	}
}

func TestDiscoveredSlugClash(t *testing.T) {
	dir := t.TempDir()
	testlib.NewAt(t, filepath.Join(dir, "My Books"))
	testlib.NewAt(t, filepath.Join(dir, "my-books"))
	r := newRegistry(t, nil, dir)
	if got := names(r.List()); len(got) != 1 {
		t.Errorf("both clashing libraries served: %q", got)
	}
}

func TestNewErrors(t *testing.T) {
	if _, err := New(nil, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing libraries_dir accepted")
	}
	if _, err := New([]config.Library{{Name: "x", Path: t.TempDir()}}, ""); err == nil {
		t.Error("configured non-library accepted")
	}
	a, b := testlib.New(t), testlib.New(t)
	if _, err := New([]config.Library{{Name: "Comics", Path: a}, {Name: "comics!", Path: b}}, ""); err == nil {
		t.Error("configured libraries with the same slug accepted")
	}
}
