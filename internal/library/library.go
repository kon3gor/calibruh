// Package library reads Calibre libraries. It never writes: Calibre's schema
// has triggers that call SQL functions only Calibre defines, so all changes go
// through calibredb instead.
package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Library struct {
	Name string
	Slug string
	Root string
	db   *sql.DB
}

type Book struct {
	ID          int64
	Title       string
	AuthorSort  string
	Authors     []string
	Series      string
	SeriesIndex float64
	Formats     []string
	HasCover    bool
	UUID        string
	Added       time.Time
	Modified    time.Time
	Published   time.Time
	path        string
}

type BookDetail struct {
	Book
	AuthorRefs  []Ref
	SeriesRef   *Ref
	Tags        []Ref
	Publisher   string
	Languages   []string
	Identifiers []Identifier
	Files       []File
	// Description is Calibre's HTML comment reduced to plain text, since it
	// comes from arbitrary uploaded files.
	Description string
}

type Ref struct {
	ID   int64
	Name string
}

type Identifier struct{ Type, Value string }

type File struct {
	Format string
	Size   int64
}

type Category struct {
	ID    int64
	Name  string
	Count int
}

// Kind is a category books can be filtered and browsed by.
type Kind struct{ table, link, col, order string }

var (
	KindAuthor = Kind{"authors", "books_authors_link", "author", "sort"}
	KindSeries = Kind{"series", "books_series_link", "series", "sort"}
	KindTag    = Kind{"tags", "books_tags_link", "tag", "name"}
)

type Sort string

const (
	SortNewest Sort = "newest"
	SortTitle  Sort = "title"
	SortAuthor Sort = "author"
)

type Query struct {
	Search   string
	Filter   Kind
	FilterID int64
	// Sort defaults to newest, or series order when filtering by series.
	Sort   Sort
	Offset int
	Limit  int
}

func Open(name, root string) (*Library, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(abs, "metadata.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("library %q: %w", name, err)
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(dbPath), RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.QueryRow(`SELECT count(*) FROM books`).Scan(new(int)); err != nil {
		db.Close()
		return nil, fmt.Errorf("library %q: not a calibre library: %w", name, err)
	}
	return &Library{Name: name, Slug: Slugify(name), Root: abs, db: db}, nil
}

func (l *Library) Close() error { return l.db.Close() }

func Slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	if slug := strings.TrimSuffix(b.String(), "-"); slug != "" {
		return slug
	}
	return "library"
}

const bookColumns = `b.id, b.title, COALESCE(b.author_sort, ''), b.path, COALESCE(b.uuid, ''),
	COALESCE(b.has_cover, 0), b.series_index,
	COALESCE(CAST(b.timestamp AS TEXT), ''), COALESCE(CAST(b.last_modified AS TEXT), ''), COALESCE(CAST(b.pubdate AS TEXT), ''),
	COALESCE((SELECT group_concat(a.name, char(31) ORDER BY l.id) FROM books_authors_link l JOIN authors a ON a.id = l.author WHERE l.book = b.id), ''),
	COALESCE((SELECT s.name FROM books_series_link l JOIN series s ON s.id = l.series WHERE l.book = b.id), ''),
	COALESCE((SELECT group_concat(d.format, char(31) ORDER BY d.format) FROM data d WHERE d.book = b.id), '')`

func scanBook(row interface{ Scan(...any) error }) (Book, error) {
	var b Book
	var added, modified, published, authors, formats string
	err := row.Scan(&b.ID, &b.Title, &b.AuthorSort, &b.path, &b.UUID, &b.HasCover, &b.SeriesIndex,
		&added, &modified, &published, &authors, &b.Series, &formats)
	b.Added, b.Modified, b.Published = parseTime(added), parseTime(modified), parseTime(published)
	b.Authors, b.Formats = splitList(authors), splitList(formats)
	return b, err
}

// Books returns one page of matching books and the total number of matches.
func (l *Library) Books(ctx context.Context, q Query) ([]Book, int, error) {
	var conds []string
	var args []any
	if q.FilterID > 0 && q.Filter != (Kind{}) {
		conds = append(conds, fmt.Sprintf("b.id IN (SELECT book FROM %s WHERE %s = ?)", q.Filter.link, q.Filter.col))
		args = append(args, q.FilterID)
	}
	for _, word := range strings.Fields(q.Search) {
		pat := "%" + likeEscaper.Replace(word) + "%"
		conds = append(conds, `(b.title LIKE ? ESCAPE '\'
			OR EXISTS (SELECT 1 FROM books_authors_link l JOIN authors a ON a.id = l.author WHERE l.book = b.id AND a.name LIKE ? ESCAPE '\')
			OR EXISTS (SELECT 1 FROM books_series_link l JOIN series s ON s.id = l.series WHERE l.book = b.id AND s.name LIKE ? ESCAPE '\')
			OR EXISTS (SELECT 1 FROM books_tags_link l JOIN tags t ON t.id = l.tag WHERE l.book = b.id AND t.name LIKE ? ESCAPE '\'))`)
		args = append(args, pat, pat, pat, pat)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	if err := l.db.QueryRowContext(ctx, "SELECT count(*) FROM books b"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	order := "b.timestamp DESC, b.id DESC"
	switch {
	case q.Sort == SortTitle:
		order = "b.sort, b.id"
	case q.Sort == SortAuthor:
		order = "b.author_sort, b.sort, b.id"
	case q.Sort == "" && q.Filter == KindSeries && q.FilterID > 0:
		order = "b.series_index, b.sort, b.id"
	}
	limit := q.Limit
	if limit <= 0 {
		limit = -1
	}
	rows, err := l.db.QueryContext(ctx, "SELECT "+bookColumns+" FROM books b"+where+" ORDER BY "+order+" LIMIT ? OFFSET ?",
		append(args, limit, max(q.Offset, 0))...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var books []Book
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, 0, err
		}
		books = append(books, b)
	}
	return books, total, rows.Err()
}

func (l *Library) Book(ctx context.Context, id int64) (*BookDetail, error) {
	b, err := scanBook(l.db.QueryRowContext(ctx, "SELECT "+bookColumns+" FROM books b WHERE b.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	d := &BookDetail{Book: b}

	if d.AuthorRefs, err = l.refs(ctx, `SELECT a.id, a.name FROM books_authors_link l JOIN authors a ON a.id = l.author WHERE l.book = ? ORDER BY l.id`, id); err != nil {
		return nil, err
	}
	series, err := l.refs(ctx, `SELECT s.id, s.name FROM books_series_link l JOIN series s ON s.id = l.series WHERE l.book = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(series) > 0 {
		d.SeriesRef = &series[0]
	}
	if d.Tags, err = l.refs(ctx, `SELECT t.id, t.name FROM books_tags_link l JOIN tags t ON t.id = l.tag WHERE l.book = ? ORDER BY t.name`, id); err != nil {
		return nil, err
	}
	if d.Publisher, err = l.optionalString(ctx, `SELECT p.name FROM books_publishers_link l JOIN publishers p ON p.id = l.publisher WHERE l.book = ?`, id); err != nil {
		return nil, err
	}
	comment, err := l.optionalString(ctx, `SELECT text FROM comments WHERE book = ?`, id)
	if err != nil {
		return nil, err
	}
	d.Description = htmlToText(comment)

	rows, err := l.db.QueryContext(ctx, `SELECT lg.lang_code FROM books_languages_link l JOIN languages lg ON lg.id = l.lang_code WHERE l.book = ? ORDER BY l.item_order`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			rows.Close()
			return nil, err
		}
		d.Languages = append(d.Languages, code)
	}
	rows.Close()

	rows, err = l.db.QueryContext(ctx, `SELECT type, val FROM identifiers WHERE book = ? ORDER BY type`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var i Identifier
		if err := rows.Scan(&i.Type, &i.Value); err != nil {
			rows.Close()
			return nil, err
		}
		d.Identifiers = append(d.Identifiers, i)
	}
	rows.Close()

	rows, err = l.db.QueryContext(ctx, `SELECT format, uncompressed_size FROM data WHERE book = ? ORDER BY format`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.Format, &f.Size); err != nil {
			return nil, err
		}
		d.Files = append(d.Files, f)
	}
	return d, rows.Err()
}

// Categories lists every author, series or tag that has at least one book.
func (l *Library) Categories(ctx context.Context, k Kind) ([]Category, error) {
	rows, err := l.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT c.id, c.name, count(l.book) FROM %s c JOIN %s l ON l.%s = c.id GROUP BY c.id ORDER BY c.%s, c.id`,
		k.table, k.link, k.col, k.order))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cats []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Count); err != nil {
			return nil, err
		}
		cats = append(cats, c)
	}
	return cats, rows.Err()
}

func (l *Library) CategoryName(ctx context.Context, k Kind, id int64) (string, error) {
	var name string
	err := l.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT name FROM %s WHERE id = ?`, k.table), id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}

// FilePath returns where a book's file in the given format lives on disk.
func (l *Library) FilePath(ctx context.Context, id int64, format string) (string, error) {
	var dir, name string
	err := l.db.QueryRowContext(ctx, `SELECT b.path, d.name FROM data d JOIN books b ON b.id = d.book WHERE d.book = ? AND d.format = ?`,
		id, strings.ToUpper(format)).Scan(&dir, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	return l.inside(dir, name+"."+strings.ToLower(format))
}

func (l *Library) CoverPath(ctx context.Context, id int64) (string, error) {
	var dir string
	var hasCover bool
	err := l.db.QueryRowContext(ctx, `SELECT path, COALESCE(has_cover, 0) FROM books WHERE id = ?`, id).Scan(&dir, &hasCover)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !hasCover) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	return l.inside(dir, "cover.jpg")
}

// inside joins a path stored in metadata.db onto the library root and refuses
// anything that would escape it.
func (l *Library) inside(dir, name string) (string, error) {
	p := filepath.Join(l.Root, filepath.FromSlash(dir), name)
	rel, err := filepath.Rel(l.Root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("library %q: path %q escapes the library", l.Name, dir)
	}
	return p, nil
}

func (l *Library) refs(ctx context.Context, query string, args ...any) ([]Ref, error) {
	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []Ref
	for rows.Next() {
		var r Ref
		if err := rows.Scan(&r.ID, &r.Name); err != nil {
			return nil, err
		}
		refs = append(refs, r)
	}
	return refs, rows.Err()
}

func (l *Library) optionalString(ctx context.Context, query string, args ...any) (string, error) {
	var s string
	err := l.db.QueryRowContext(ctx, query, args...).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return s, err
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x1f")
}

// parseTime reads Calibre's timestamps. Calibre stores unknown dates as year
// 101, which comes back as the zero time.
func parseTime(s string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05Z07:00", "2006-01-02T15:04:05Z07:00", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			if t.Year() <= 101 {
				return time.Time{}
			}
			return t
		}
	}
	return time.Time{}
}

var (
	blockTagRe = regexp.MustCompile(`(?i)<\s*(br\s*/?|/p|/div|/li|/h[1-6])\s*>`)
	tagRe      = regexp.MustCompile(`<[^>]*>`)
	blankRe    = regexp.MustCompile(`\n\s*\n\s*`)
)

func htmlToText(s string) string {
	s = blockTagRe.ReplaceAllString(s, "\n")
	s = html.UnescapeString(tagRe.ReplaceAllString(s, ""))
	return strings.TrimSpace(blankRe.ReplaceAllString(s, "\n\n"))
}
