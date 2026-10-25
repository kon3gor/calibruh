// Package testlib builds small Calibre-shaped libraries for tests. The schema
// is the subset of Calibre's metadata.db that calibruh reads, without the
// triggers that need Calibre's custom SQL functions.
package testlib

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE books (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL DEFAULT 'Unknown' COLLATE NOCASE,
	sort TEXT COLLATE NOCASE, timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP, pubdate TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	series_index REAL NOT NULL DEFAULT 1.0, author_sort TEXT COLLATE NOCASE, path TEXT NOT NULL DEFAULT '', uuid TEXT,
	has_cover BOOL DEFAULT 0, last_modified TIMESTAMP NOT NULL DEFAULT '2000-01-01 00:00:00+00:00');
CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE, sort TEXT COLLATE NOCASE, link TEXT NOT NULL DEFAULT '', UNIQUE(name));
CREATE TABLE books_authors_link (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, author INTEGER NOT NULL, UNIQUE(book, author));
CREATE TABLE series (id INTEGER PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE, sort TEXT COLLATE NOCASE, link TEXT NOT NULL DEFAULT '', UNIQUE(name));
CREATE TABLE books_series_link (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, series INTEGER NOT NULL, UNIQUE(book));
CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE, link TEXT NOT NULL DEFAULT '', UNIQUE(name));
CREATE TABLE books_tags_link (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, tag INTEGER NOT NULL, UNIQUE(book, tag));
CREATE TABLE publishers (id INTEGER PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE, sort TEXT COLLATE NOCASE, link TEXT NOT NULL DEFAULT '', UNIQUE(name));
CREATE TABLE books_publishers_link (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, publisher INTEGER NOT NULL, UNIQUE(book));
CREATE TABLE languages (id INTEGER PRIMARY KEY, lang_code TEXT NOT NULL COLLATE NOCASE, link TEXT NOT NULL DEFAULT '', UNIQUE(lang_code));
CREATE TABLE books_languages_link (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, lang_code INTEGER NOT NULL, item_order INTEGER NOT NULL DEFAULT 0, UNIQUE(book, lang_code));
CREATE TABLE identifiers (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, type TEXT NOT NULL DEFAULT 'isbn' COLLATE NOCASE, val TEXT NOT NULL COLLATE NOCASE, UNIQUE(book, type));
CREATE TABLE comments (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, text TEXT NOT NULL COLLATE NOCASE, UNIQUE(book));
CREATE TABLE data (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, format TEXT NOT NULL COLLATE NOCASE, uncompressed_size INTEGER NOT NULL, name TEXT NOT NULL, UNIQUE(book, format));
`

// Books, newest first:
//
//	2 The Fellowship of the Ring  Tolkien             Middle-earth #2  EPUB, PDF
//	3 Dune                        Herbert             -                MOBI, no cover
//	1 The Hobbit                  Tolkien             Middle-earth #1  EPUB
//	4 Good Omens                  Pratchett, Gaiman   -                EPUB, unknown pubdate
const fixture = `
INSERT INTO authors (id, name, sort) VALUES (1, 'J. R. R. Tolkien', 'Tolkien, J. R. R.'), (2, 'Frank Herbert', 'Herbert, Frank'),
	(3, 'Neil Gaiman', 'Gaiman, Neil'), (4, 'Terry Pratchett', 'Pratchett, Terry');
INSERT INTO books (id, title, sort, author_sort, timestamp, pubdate, last_modified, series_index, path, uuid, has_cover) VALUES
	(1, 'The Hobbit', 'Hobbit, The', 'Tolkien, J. R. R.', '2024-01-01 10:00:00+00:00', '1937-09-21 00:00:00+00:00', '2024-01-05 10:00:00.123456+00:00', 1, 'J. R. R. Tolkien/The Hobbit (1)', 'uuid-hobbit', 1),
	(2, 'The Fellowship of the Ring', 'Fellowship of the Ring, The', 'Tolkien, J. R. R.', '2024-03-01 10:00:00+00:00', '1954-07-29 00:00:00+00:00', '2024-03-01 10:00:00+00:00', 2, 'J. R. R. Tolkien/The Fellowship of the Ring (2)', 'uuid-fellowship', 1),
	(3, 'Dune', 'Dune', 'Herbert, Frank', '2024-02-01 10:00:00+00:00', '1965-08-01 00:00:00+00:00', '2024-02-01 10:00:00+00:00', 1, 'Frank Herbert/Dune (3)', 'uuid-dune', 0),
	(4, 'Good Omens', 'Good Omens', 'Pratchett, Terry & Gaiman, Neil', '2023-12-01 10:00:00+00:00', '0101-01-01 00:00:00+00:00', '2023-12-01 10:00:00+00:00', 1, 'Terry Pratchett/Good Omens (4)', 'uuid-omens', 1);
INSERT INTO books_authors_link (id, book, author) VALUES (1, 1, 1), (2, 2, 1), (3, 3, 2), (4, 4, 4), (5, 4, 3);
INSERT INTO series (id, name, sort) VALUES (1, 'Middle-earth', 'Middle-earth');
INSERT INTO books_series_link (book, series) VALUES (1, 1), (2, 1);
INSERT INTO tags (id, name) VALUES (1, 'fantasy'), (2, 'classic'), (3, 'scifi');
INSERT INTO books_tags_link (book, tag) VALUES (1, 1), (1, 2), (2, 1), (3, 3), (4, 1);
INSERT INTO publishers (id, name) VALUES (1, 'Allen & Unwin');
INSERT INTO books_publishers_link (book, publisher) VALUES (1, 1);
INSERT INTO languages (id, lang_code) VALUES (1, 'eng');
INSERT INTO books_languages_link (book, lang_code) VALUES (1, 1);
INSERT INTO identifiers (book, type, val) VALUES (1, 'isbn', '9780261103344');
INSERT INTO comments (book, text) VALUES (1, '<p>In a hole in the ground &amp; all</p><p>lived a <b>hobbit</b>.</p><script>alert(1)</script>');
INSERT INTO data (book, format, uncompressed_size, name) VALUES
	(1, 'EPUB', 11, 'The Hobbit - J. R. R. Tolkien'),
	(2, 'EPUB', 15, 'The Fellowship of the Ring - J. R. R. Tolkien'), (2, 'PDF', 14, 'The Fellowship of the Ring - J. R. R. Tolkien'),
	(3, 'MOBI', 9, 'Dune - Frank Herbert'),
	(4, 'EPUB', 10, 'Good Omens - Terry Pratchett');
`

// New creates a library in a temp dir and returns its root.
func New(t testing.TB) string {
	t.Helper()
	return NewAt(t, t.TempDir())
}

// NewAt creates a library at root, which may not exist yet.
func NewAt(t testing.TB, root string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schema + fixture); err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		"J. R. R. Tolkien/The Hobbit (1)/The Hobbit - J. R. R. Tolkien.epub":                                 "hobbit epub",
		"J. R. R. Tolkien/The Hobbit (1)/cover.jpg":                                                          "hobbit cover",
		"J. R. R. Tolkien/The Fellowship of the Ring (2)/The Fellowship of the Ring - J. R. R. Tolkien.epub": "fellowship epub",
		"J. R. R. Tolkien/The Fellowship of the Ring (2)/The Fellowship of the Ring - J. R. R. Tolkien.pdf":  "fellowship pdf",
		"J. R. R. Tolkien/The Fellowship of the Ring (2)/cover.jpg":                                          "fellowship cover",
		"Frank Herbert/Dune (3)/Dune - Frank Herbert.mobi":                                                   "dune mobi",
		"Terry Pratchett/Good Omens (4)/Good Omens - Terry Pratchett.epub":                                   "omens epub",
		"Terry Pratchett/Good Omens (4)/cover.jpg":                                                           "omens cover",
	}
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Exec runs SQL against a library created by New.
func Exec(t testing.TB, root, query string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}
