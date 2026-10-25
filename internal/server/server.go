// Package server serves the web UI and OPDS feeds for a set of libraries.
package server

import (
	"crypto/subtle"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"calibruh/internal/calibredb"
	"calibruh/internal/config"
	"calibruh/internal/libraries"
	"calibruh/internal/library"
	"calibruh/web"
)

type Server struct {
	cfg       config.Config
	libs      *libraries.Registry
	calibredb *calibredb.Runner
	pages     map[string]*template.Template
}

func New(cfg config.Config, libs *libraries.Registry, runner *calibredb.Runner) (*Server, error) {
	s := &Server{cfg: cfg, libs: libs, calibredb: runner, pages: map[string]*template.Template{}}
	for _, page := range []string{"books", "book", "categories", "upload", "empty"} {
		t, err := template.New("").Funcs(templateFuncs).ParseFS(web.FS, "templates/layout.html", "templates/"+page+".html")
		if err != nil {
			return nil, err
		}
		s.pages[page] = t
	}
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(web.FS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	mux.HandleFunc("GET /{$}", s.handleRoot)
	mux.HandleFunc("GET /lib/{lib}/{$}", s.handleBooks)
	for _, c := range categories {
		mux.HandleFunc("GET /lib/{lib}/"+c.Path, s.handleCategories(c))
	}
	mux.HandleFunc("GET /lib/{lib}/books/{id}", s.handleBook)
	mux.HandleFunc("GET /lib/{lib}/books/{id}/cover", s.handleCover)
	mux.HandleFunc("GET /lib/{lib}/books/{id}/file/{format}", s.handleDownload)
	mux.HandleFunc("GET /lib/{lib}/upload", s.handleUploadForm)
	mux.HandleFunc("POST /lib/{lib}/upload", s.handleUpload)

	mux.HandleFunc("GET /opds", s.opdsRoot)
	mux.HandleFunc("GET /opds/{lib}/{$}", s.opdsLibrary)
	mux.HandleFunc("GET /opds/{lib}/opensearch.xml", s.opdsOpenSearch)
	mux.HandleFunc("GET /opds/{lib}/new", s.opdsBooks("Newest", library.SortNewest))
	mux.HandleFunc("GET /opds/{lib}/titles", s.opdsBooks("All titles", library.SortTitle))
	mux.HandleFunc("GET /opds/{lib}/search", s.opdsSearch)
	for _, c := range categories {
		mux.HandleFunc("GET /opds/{lib}/"+c.Path, s.opdsCategories(c))
		mux.HandleFunc("GET /opds/{lib}/"+c.Path+"/{id}", s.opdsCategoryBooks(c))
	}

	return logRequests(s.auth(sameOrigin(mux)))
}

// category ties a library.Kind to its URL path and query parameter.
type category struct {
	Path  string
	Param string
	Label string
	Kind  library.Kind
}

var categories = []category{
	{"authors", "author", "Authors", library.KindAuthor},
	{"series", "series", "Series", library.KindSeries},
	{"tags", "tag", "Tags", library.KindTag},
}

func (s *Server) library(w http.ResponseWriter, r *http.Request) *library.Library {
	l := s.libs.Get(r.PathValue("lib"))
	if l == nil {
		http.NotFound(w, r)
	}
	return l
}

func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	lib := s.library(w, r)
	if lib == nil {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	path, err := lib.CoverPath(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	serveFile(w, r, path, "image/jpeg", "")
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	lib := s.library(w, r)
	if lib == nil {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	format := strings.ToLower(r.PathValue("format"))
	path, err := lib.FilePath(r.Context(), id, format)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	book, err := lib.Book(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	name := book.Title
	if len(book.Authors) > 0 {
		name += " - " + book.Authors[0]
	}
	serveFile(w, r, path, mimeType(format), name+"."+format)
}

func serveFile(w http.ResponseWriter, r *http.Request, path, contentType, downloadName string) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "cannot open file", http.StatusInternalServerError)
		}
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "cannot open file", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	if downloadName != "" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": downloadName}))
	}
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), f)
}

var mimeTypes = map[string]string{
	"epub":          "application/epub+zip",
	"kepub":         "application/kepub+zip",
	"pdf":           "application/pdf",
	"mobi":          "application/x-mobipocket-ebook",
	"azw":           "application/vnd.amazon.ebook",
	"azw3":          "application/vnd.amazon.mobi8-ebook",
	"kfx":           "application/vnd.amazon.ebook",
	"fb2":           "application/x-fictionbook+xml",
	"djvu":          "image/vnd.djvu",
	"cbz":           "application/vnd.comicbook+zip",
	"cbr":           "application/vnd.comicbook-rar",
	"cb7":           "application/x-cb7",
	"txt":           "text/plain; charset=utf-8",
	"rtf":           "application/rtf",
	"html":          "text/html; charset=utf-8",
	"docx":          "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"odt":           "application/vnd.oasis.opendocument.text",
	"lit":           "application/x-ms-reader",
	"lrf":           "application/x-sony-bbeb",
	"pdb":           "application/vnd.palm",
	"original_epub": "application/epub+zip",
}

func mimeType(format string) string {
	if t, ok := mimeTypes[strings.ToLower(format)]; ok {
		return t
	}
	return "application/octet-stream"
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}

func pageParam(r *http.Request) int {
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 1 {
		return p
	}
	return 1
}

// withQuery returns the request path with some query parameters replaced;
// empty values are dropped.
func withQuery(r *http.Request, kv ...string) string {
	q := r.URL.Query()
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			q.Del(kv[i])
		} else {
			q.Set(kv[i], kv[i+1])
		}
	}
	u := url.URL{Path: r.URL.Path, RawQuery: q.Encode()}
	return u.String()
}

// fail maps library errors to responses; anything unexpected is logged and
// reported as a 500 without details.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, library.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	slog.Error("request failed", "path", r.URL.Path, "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (s *Server) auth(next http.Handler) http.Handler {
	if s.cfg.Username == "" {
		return next
	}
	user, pass := []byte(s.cfg.Username), []byte(s.cfg.Password)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(u), user)&subtle.ConstantTimeCompare([]byte(p), pass) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="calibruh", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin rejects cross-site POSTs. Browsers resend Basic auth credentials
// on their own, so without this any site could upload into the library.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
					http.Error(w, "cross-origin request refused", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "took", time.Since(start).Round(time.Millisecond))
	})
}
