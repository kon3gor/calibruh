package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"calibruh/internal/testlib"
)

func open(t *testing.T) *Library {
	t.Helper()
	l, err := Open("My Books", testlib.New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func titles(books []Book) []string {
	var out []string
	for _, b := range books {
		out = append(out, b.Title)
	}
	return out
}

func TestBooks(t *testing.T) {
	l := open(t)
	tests := []struct {
		name  string
		q     Query
		want  []string
		total int
	}{
		{"newest by default", Query{}, []string{"The Fellowship of the Ring", "Dune", "The Hobbit", "Good Omens"}, 4},
		{"by title uses sort column", Query{Sort: SortTitle}, []string{"Dune", "The Fellowship of the Ring", "Good Omens", "The Hobbit"}, 4},
		{"by author", Query{Sort: SortAuthor}, []string{"Dune", "Good Omens", "The Fellowship of the Ring", "The Hobbit"}, 4},
		{"page", Query{Limit: 2, Offset: 2}, []string{"The Hobbit", "Good Omens"}, 4},
		{"series filter uses series order", Query{Filter: KindSeries, FilterID: 1}, []string{"The Hobbit", "The Fellowship of the Ring"}, 2},
		{"author filter", Query{Filter: KindAuthor, FilterID: 3}, []string{"Good Omens"}, 1},
		{"tag filter", Query{Filter: KindTag, FilterID: 1, Sort: SortTitle}, []string{"The Fellowship of the Ring", "Good Omens", "The Hobbit"}, 3},
		{"search matches every word", Query{Search: "tolkien hobbit"}, []string{"The Hobbit"}, 1},
		{"search is case-insensitive and covers tags", Query{Search: "SCIFI"}, []string{"Dune"}, 1},
		{"search covers series", Query{Search: "middle-earth", Sort: SortTitle}, []string{"The Fellowship of the Ring", "The Hobbit"}, 2},
		{"search escapes LIKE wildcards", Query{Search: "%"}, nil, 0},
		{"search within filter", Query{Filter: KindAuthor, FilterID: 1, Search: "ring"}, []string{"The Fellowship of the Ring"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			books, total, err := l.Books(context.Background(), tt.q)
			if err != nil {
				t.Fatal(err)
			}
			if got := titles(books); !reflect.DeepEqual(got, tt.want) || total != tt.total {
				t.Errorf("got %q (total %d), want %q (total %d)", got, total, tt.want, tt.total)
			}
		})
	}
}

func TestBookFields(t *testing.T) {
	l := open(t)
	books, _, err := l.Books(context.Background(), Query{Sort: SortTitle})
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]Book{}
	for _, b := range books {
		byTitle[b.Title] = b
	}

	omens := byTitle["Good Omens"]
	if want := []string{"Terry Pratchett", "Neil Gaiman"}; !reflect.DeepEqual(omens.Authors, want) {
		t.Errorf("authors = %q, want %q in link order", omens.Authors, want)
	}
	if !omens.Published.IsZero() {
		t.Errorf("year-101 pubdate should be unknown, got %v", omens.Published)
	}

	fellowship := byTitle["The Fellowship of the Ring"]
	if !reflect.DeepEqual(fellowship.Formats, []string{"EPUB", "PDF"}) || fellowship.Series != "Middle-earth" || fellowship.SeriesIndex != 2 {
		t.Errorf("fellowship = %+v", fellowship)
	}
	if !fellowship.HasCover || byTitle["Dune"].HasCover {
		t.Error("has_cover not read")
	}
	if got := byTitle["The Hobbit"].Modified.Format("2006-01-02 15:04:05.000000"); got != "2024-01-05 10:00:00.123456" {
		t.Errorf("modified = %s", got)
	}
}

func TestBook(t *testing.T) {
	l := open(t)
	d, err := l.Book(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if d.Publisher != "Allen & Unwin" || !reflect.DeepEqual(d.Languages, []string{"eng"}) {
		t.Errorf("publisher/languages = %q %q", d.Publisher, d.Languages)
	}
	if d.SeriesRef == nil || d.SeriesRef.Name != "Middle-earth" {
		t.Errorf("series = %+v", d.SeriesRef)
	}
	if want := []Ref{{2, "classic"}, {1, "fantasy"}}; !reflect.DeepEqual(d.Tags, want) {
		t.Errorf("tags = %+v", d.Tags)
	}
	if want := []Identifier{{"isbn", "9780261103344"}}; !reflect.DeepEqual(d.Identifiers, want) {
		t.Errorf("identifiers = %+v", d.Identifiers)
	}
	if want := []File{{"EPUB", 11}}; !reflect.DeepEqual(d.Files, want) {
		t.Errorf("files = %+v", d.Files)
	}
	if want := "In a hole in the ground & all\nlived a hobbit.\nalert(1)"; d.Description != want {
		t.Errorf("description = %q, want %q", d.Description, want)
	}

	if _, err := l.Book(context.Background(), 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing book: err = %v", err)
	}
}

func TestCategories(t *testing.T) {
	l := open(t)
	authors, err := l.Categories(context.Background(), KindAuthor)
	if err != nil {
		t.Fatal(err)
	}
	want := []Category{{3, "Neil Gaiman", 1}, {2, "Frank Herbert", 1}, {4, "Terry Pratchett", 1}, {1, "J. R. R. Tolkien", 2}}
	if !reflect.DeepEqual(authors, want) {
		t.Errorf("authors = %+v", authors)
	}
	name, err := l.CategoryName(context.Background(), KindSeries, 1)
	if err != nil || name != "Middle-earth" {
		t.Errorf("series name = %q, %v", name, err)
	}
	if _, err := l.CategoryName(context.Background(), KindTag, 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing tag: err = %v", err)
	}
}

func TestFiles(t *testing.T) {
	l := open(t)
	ctx := context.Background()

	p, err := l.FilePath(ctx, 2, "pdf")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != "fellowship pdf" {
		t.Errorf("pdf content = %q", got)
	}
	if _, err := l.FilePath(ctx, 2, "mobi"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing format: err = %v", err)
	}
	if p, err := l.CoverPath(ctx, 1); err != nil || filepath.Base(p) != "cover.jpg" {
		t.Errorf("cover = %q, %v", p, err)
	}
	if _, err := l.CoverPath(ctx, 3); !errors.Is(err, ErrNotFound) {
		t.Errorf("book without cover: err = %v", err)
	}

	testlib.Exec(t, l.Root, `UPDATE books SET path = '../../etc' WHERE id = 3`)
	if _, err := l.FilePath(ctx, 3, "mobi"); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("path escaping the library: err = %v", err)
	}
}

func TestOpenIsReadOnly(t *testing.T) {
	l := open(t)
	if _, err := l.db.Exec(`DELETE FROM books`); err == nil {
		t.Fatal("write through the library connection succeeded")
	}
}

func TestOpenPathWithSpaces(t *testing.T) {
	src := testlib.New(t)
	dst := filepath.Join(t.TempDir(), "Calibre Library #1")
	if err := os.Rename(src, dst); err != nil {
		t.Fatal(err)
	}
	l, err := Open("x", dst)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, total, err := l.Books(context.Background(), Query{}); err != nil || total != 4 {
		t.Errorf("total = %d, %v", total, err)
	}
}

func TestOpenRejectsNonLibrary(t *testing.T) {
	if _, err := Open("x", t.TempDir()); err == nil {
		t.Error("opened a directory without metadata.db")
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{"My Books": "my-books", "  Comics & Manga!! ": "comics-manga", "Книги": "книги", "!!!": "library"} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
