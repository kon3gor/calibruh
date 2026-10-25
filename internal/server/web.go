package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"calibruh/internal/library"
)

const webPageSize = 48

type pageData struct {
	Title     string
	Libraries []*library.Library
	Lib       *library.Library
	Nav       string
	Search    string
	Body      any
}

var templateFuncs = template.FuncMap{
	"join":  strings.Join,
	"lower": strings.ToLower,
	"upper": strings.ToUpper,
	"date": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format("2 Jan 2006")
	},
	"size": func(n int64) string {
		switch {
		case n >= 1<<20:
			return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
		case n >= 1<<10:
			return fmt.Sprintf("%d KB", n>>10)
		}
		return fmt.Sprintf("%d B", n)
	},
	"seriesIndex": func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) },
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	libs := s.libs.List()
	if len(libs) == 0 {
		s.render(w, http.StatusOK, "empty", pageData{Title: "No libraries", Body: s.cfg.LibrariesDir})
		return
	}
	http.Redirect(w, r, "/lib/"+libs[0].Slug+"/", http.StatusFound)
}

func (s *Server) render(w http.ResponseWriter, status int, page string, data pageData) {
	data.Libraries = s.libs.List()
	var buf bytes.Buffer
	if err := s.pages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		slog.Error("render failed", "page", page, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

type sortTab struct {
	Label, URL string
	Active     bool
}

type booksBody struct {
	Heading string
	Total   int
	Books   []library.Book
	Sorts   []sortTab
	Page    int
	Pages   int
	PrevURL string
	NextURL string
	Empty   string
}

func (s *Server) handleBooks(w http.ResponseWriter, r *http.Request) {
	lib := s.library(w, r)
	if lib == nil {
		return
	}
	ctx := r.Context()
	params := r.URL.Query()
	q := library.Query{Search: strings.TrimSpace(params.Get("q")), Sort: library.Sort(params.Get("sort"))}
	heading := "All books"
	for _, c := range categories {
		if id, err := strconv.ParseInt(params.Get(c.Param), 10, 64); err == nil && id > 0 {
			name, err := lib.CategoryName(ctx, c.Kind, id)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			q.Filter, q.FilterID, heading = c.Kind, id, name
			break
		}
	}
	if q.Search != "" {
		heading = fmt.Sprintf("Results for “%s”", q.Search)
	}

	page := pageParam(r)
	q.Limit, q.Offset = webPageSize, (page-1)*webPageSize
	books, total, err := lib.Books(ctx, q)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	body := booksBody{Heading: heading, Total: total, Books: books, Page: page, Pages: (total + webPageSize - 1) / webPageSize}
	sorts := []struct {
		label string
		sort  library.Sort
	}{{"Newest", ""}, {"Title", library.SortTitle}, {"Author", library.SortAuthor}}
	if q.Filter == library.KindSeries {
		sorts[0].label = "Series order"
	}
	for _, t := range sorts {
		body.Sorts = append(body.Sorts, sortTab{t.label, withQuery(r, "sort", string(t.sort), "page", ""), q.Sort == t.sort})
	}
	if page > 1 {
		body.PrevURL = withQuery(r, "page", strconv.Itoa(page-1))
	}
	if page < body.Pages {
		body.NextURL = withQuery(r, "page", strconv.Itoa(page+1))
	}
	switch {
	case q.Search != "":
		body.Empty = "Nothing matches that search."
	case total == 0:
		body.Empty = "This library is empty. Upload some books to get started."
	}
	s.render(w, http.StatusOK, "books", pageData{Title: heading, Lib: lib, Nav: "books", Search: q.Search, Body: body})
}

type categoriesBody struct {
	Category category
	Items    []library.Category
}

func (s *Server) handleCategories(c category) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lib := s.library(w, r)
		if lib == nil {
			return
		}
		items, err := lib.Categories(r.Context(), c.Kind)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.render(w, http.StatusOK, "categories", pageData{Title: c.Label, Lib: lib, Nav: c.Path, Body: categoriesBody{c, items}})
	}
}

func (s *Server) handleBook(w http.ResponseWriter, r *http.Request) {
	lib := s.library(w, r)
	if lib == nil {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	book, err := lib.Book(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, http.StatusOK, "book", pageData{Title: book.Title, Lib: lib, Nav: "books", Body: book})
}

// uploadFormats are the extensions calibredb add accepts as books.
var uploadFormats = map[string]bool{
	"epub": true, "kepub": true, "pdf": true, "mobi": true, "azw": true, "azw3": true, "azw4": true, "kfx": true,
	"fb2": true, "fbz": true, "djvu": true, "djv": true, "cbz": true, "cbr": true, "cb7": true, "cbc": true,
	"txt": true, "txtz": true, "rtf": true, "docx": true, "odt": true, "html": true, "htm": true, "htmlz": true,
	"lit": true, "lrf": true, "pdb": true, "pml": true, "rb": true, "snb": true, "tcr": true, "chm": true,
}

type uploadBody struct {
	Accept  string
	MaxMB   int64
	Error   string
	Results []uploadResult
}

type uploadResult struct {
	File    string
	Status  string // "added", "duplicate" or "error"
	BookID  int64
	Message string
}

func (s *Server) uploadBody() uploadBody {
	exts := make([]string, 0, len(uploadFormats))
	for ext := range uploadFormats {
		exts = append(exts, "."+ext)
	}
	return uploadBody{Accept: strings.Join(exts, ","), MaxMB: s.cfg.MaxUploadMB}
}

func (s *Server) handleUploadForm(w http.ResponseWriter, r *http.Request) {
	lib := s.library(w, r)
	if lib == nil {
		return
	}
	s.render(w, http.StatusOK, "upload", pageData{Title: "Upload", Lib: lib, Nav: "upload", Body: s.uploadBody()})
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	lib := s.library(w, r)
	if lib == nil {
		return
	}
	body := s.uploadBody()
	page := pageData{Title: "Upload", Lib: lib, Nav: "upload"}

	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadMB<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		status := http.StatusBadRequest
		body.Error = "Could not read the upload."
		if errors.As(err, new(*http.MaxBytesError)) {
			status = http.StatusRequestEntityTooLarge
			body.Error = fmt.Sprintf("The upload is larger than %d MB.", s.cfg.MaxUploadMB)
		}
		page.Body = body
		s.render(w, status, "upload", page)
		return
	}
	defer r.MultipartForm.RemoveAll()

	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		body.Error = "Pick at least one file."
		page.Body = body
		s.render(w, http.StatusBadRequest, "upload", page)
		return
	}
	dir, err := os.MkdirTemp("", "calibruh-upload-*")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer os.RemoveAll(dir)

	// A client that disconnects must not kill calibredb halfway through
	// writing to the library; the runner's own timeout still applies.
	ctx := context.WithoutCancel(r.Context())
	for i, fh := range files {
		body.Results = append(body.Results, s.importFile(ctx, lib, filepath.Join(dir, strconv.Itoa(i)), fh))
	}
	page.Body = body
	s.render(w, http.StatusOK, "upload", page)
}

func (s *Server) importFile(ctx context.Context, lib *library.Library, dir string, fh *multipart.FileHeader) uploadResult {
	name := cleanFilename(fh.Filename)
	res := uploadResult{File: name, Status: "error"}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if !uploadFormats[ext] {
		res.Message = "Not an ebook format calibre can import."
		return res
	}
	dst := filepath.Join(dir, name)
	if err := saveUpload(fh, dst); err != nil {
		slog.Error("saving upload failed", "file", name, "err", err)
		res.Message = "Could not save the file."
		return res
	}
	out, err := s.calibredb.Add(ctx, lib.Root, dst)
	switch {
	case err != nil:
		slog.Error("calibredb add failed", "file", name, "err", err, "output", out.Output)
		res.Message = err.Error()
	case len(out.IDs) > 0:
		res.Status, res.BookID = "added", out.IDs[0]
	case out.Duplicate:
		res.Status, res.Message = "duplicate", "A book with this title and author is already in the library."
	default:
		slog.Error("calibredb add reported nothing", "file", name, "output", out.Output)
		res.Message = "calibredb did not report an added book."
	}
	return res
}

func saveUpload(fh *multipart.FileHeader, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	src, err := fh.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, src); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// cleanFilename keeps the uploaded name, which calibre falls back on for the
// title, but strips any directory parts.
func cleanFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.TrimLeft(name, ".")
	if name == "" || name == "/" {
		return "upload"
	}
	return name
}
