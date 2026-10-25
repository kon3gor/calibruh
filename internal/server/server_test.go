package server

import (
	"bytes"
	"encoding/xml"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"calibruh/internal/calibredb"
	"calibruh/internal/config"
	"calibruh/internal/libraries"
	"calibruh/internal/testlib"
)

type fixture struct {
	handler http.Handler
	root    string
	argsLog string
}

// newFixture serves one test library. calibredb is replaced by a script that
// logs its arguments and prints whatever is in $FAKE_CALIBREDB_OUTPUT.
func newFixture(t *testing.T, mutate func(*config.Config)) fixture {
	t.Helper()
	root := testlib.New(t)
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args")
	script := filepath.Join(dir, "calibredb")
	body := "#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done > " + argsLog + "\nprintf '%s\\n' \"$FAKE_CALIBREDB_OUTPUT\"\nexit ${FAKE_CALIBREDB_EXIT:-0}\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{Addr: ":0", Calibredb: script, MaxUploadMB: 1, Libraries: []config.Library{{Name: "Books", Path: root}}}
	if mutate != nil {
		mutate(&cfg)
	}
	libs := registry(t, cfg)
	srv, err := New(cfg, libs, &calibredb.Runner{Bin: script, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{srv.Handler(), root, argsLog}
}

func registry(t *testing.T, cfg config.Config) *libraries.Registry {
	t.Helper()
	libs, err := libraries.New(cfg.Libraries, cfg.LibrariesDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(libs.Close)
	return libs
}

func (f fixture) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func parseFeed(t *testing.T, rec *httptest.ResponseRecorder) feed {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var f feed
	if err := xml.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatalf("invalid feed: %v\n%s", err, rec.Body)
	}
	return f
}

func TestOPDSRootServesSingleLibraryDirectly(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.get(t, "/opds")
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "kind=navigation") {
		t.Errorf("content type = %q", ct)
	}
	feed := parseFeed(t, rec)
	var hrefs []string
	for _, e := range feed.Entries {
		hrefs = append(hrefs, e.Links[0].Href)
	}
	want := "/opds/books/new /opds/books/titles /opds/books/authors /opds/books/series /opds/books/tags"
	if got := strings.Join(hrefs, " "); got != want {
		t.Errorf("entries = %s", got)
	}
	if !strings.Contains(rec.Body.String(), `href="/opds/books/search?q={searchTerms}"`) {
		t.Error("missing search template link")
	}
}

func TestOPDSAcquisition(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.get(t, "/opds/books/new")
	body := rec.Body.String()
	feed := parseFeed(t, rec)
	if len(feed.Entries) != 4 || feed.Entries[0].Title != "The Fellowship of the Ring" {
		t.Fatalf("entries = %+v", feed.Entries)
	}
	for _, want := range []string{
		`<link rel="http://opds-spec.org/acquisition" href="/lib/books/books/2/file/epub" type="application/epub+zip" title="EPUB">`,
		`<link rel="http://opds-spec.org/acquisition" href="/lib/books/books/2/file/pdf" type="application/pdf" title="PDF">`,
		`<link rel="http://opds-spec.org/image/thumbnail" href="/lib/books/books/2/cover" type="image/jpeg">`,
		`<id>urn:uuid:uuid-fellowship</id>`,
		`<dc:issued>1954-07-29</dc:issued>`,
		`<content type="text">Book 2 of Middle-earth</content>`,
		`xmlns:dc="http://purl.org/dc/terms/"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("feed lacks %s", want)
		}
	}
	dune := feed.Entries[1]
	for _, l := range dune.Links {
		if strings.Contains(l.Rel, "image") {
			t.Errorf("Dune has no cover but got %+v", l)
		}
	}
	if strings.Contains(body, `rel="next"`) {
		t.Error("single page feed has a next link")
	}
}

func TestOPDSSearchAndCategories(t *testing.T) {
	f := newFixture(t, nil)
	if e := parseFeed(t, f.get(t, "/opds/books/search?q=dune")).Entries; len(e) != 1 || e[0].Title != "Dune" {
		t.Errorf("search = %+v", e)
	}

	series := parseFeed(t, f.get(t, "/opds/books/series"))
	if len(series.Entries) != 1 || series.Entries[0].Links[0].Href != "/opds/books/series/1" || series.Entries[0].Content.Body != "2 books" {
		t.Fatalf("series = %+v", series.Entries)
	}
	books := parseFeed(t, f.get(t, "/opds/books/series/1"))
	if books.Title != "Middle-earth" || len(books.Entries) != 2 || books.Entries[0].Title != "The Hobbit" {
		t.Errorf("series books = %+v", books)
	}
	if rec := f.get(t, "/opds/books/authors/99"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown author: status %d", rec.Code)
	}
	if rec := f.get(t, "/opds/nope/new"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown library: status %d", rec.Code)
	}
}

func TestDownloadAndCover(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.get(t, "/lib/books/books/2/file/pdf")
	if rec.Code != http.StatusOK || rec.Body.String() != "fellowship pdf" {
		t.Fatalf("download: %d %q", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("content type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "The Fellowship of the Ring - J. R. R. Tolkien.pdf") {
		t.Errorf("content disposition = %q", cd)
	}
	if rec := f.get(t, "/lib/books/books/2/file/mobi"); rec.Code != http.StatusNotFound {
		t.Errorf("missing format: %d", rec.Code)
	}
	if rec := f.get(t, "/lib/books/books/1/cover"); rec.Code != http.StatusOK || rec.Body.String() != "hobbit cover" {
		t.Errorf("cover: %d %q", rec.Code, rec.Body)
	}
	if rec := f.get(t, "/lib/books/books/3/cover"); rec.Code != http.StatusNotFound {
		t.Errorf("no cover: %d", rec.Code)
	}
}

func TestPagesRender(t *testing.T) {
	f := newFixture(t, nil)
	if rec := f.get(t, "/"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/lib/books/" {
		t.Errorf("root: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	for path, want := range map[string]string{
		"/lib/books/":                "The Fellowship of the Ring",
		"/lib/books/?series=1":       "Series order",
		"/lib/books/?q=nothing-here": "Nothing matches that search.",
		"/lib/books/authors":         "J. R. R. Tolkien",
		"/lib/books/tags":            "scifi",
		"/lib/books/books/1":         "Allen &amp; Unwin",
		"/lib/books/upload":          "Drop books here",
	} {
		rec := f.get(t, path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: status %d, want body containing %q", path, rec.Code, want)
		}
	}
	if body := f.get(t, "/lib/books/books/1").Body.String(); strings.Contains(body, "<script>alert") || strings.Contains(body, "<b>hobbit") {
		t.Error("book description HTML was rendered")
	}
}

func upload(t *testing.T, h http.Handler, files map[string]string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, content := range files {
		w, _ := mw.CreateFormFile("files", name)
		io.WriteString(w, content)
	}
	mw.Close()
	req := httptest.NewRequest("POST", "/lib/books/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestUpload(t *testing.T) {
	f := newFixture(t, nil)
	t.Setenv("FAKE_CALIBREDB_OUTPUT", "Added book ids: 42")
	rec := upload(t, f.handler, map[string]string{"../../Neuromancer.epub": "epub bytes"}, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `href="/lib/books/books/42"`) {
		t.Fatalf("status %d:\n%s", rec.Code, rec.Body)
	}
	raw, err := os.ReadFile(f.argsLog)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(args) != 5 || args[0] != "add" || args[1] != "--with-library" || args[2] != f.root || args[3] != "--" || filepath.Base(args[4]) != "Neuromancer.epub" {
		t.Errorf("calibredb args = %q", args)
	}
	if _, err := os.Stat(args[4]); !os.IsNotExist(err) {
		t.Error("temporary upload was not removed")
	}
}

func TestUploadOutcomes(t *testing.T) {
	f := newFixture(t, nil)

	t.Setenv("FAKE_CALIBREDB_OUTPUT", "The following books were not added as they already exist in the database (see --duplicates option):\n  Dune")
	if body := upload(t, f.handler, map[string]string{"Dune.epub": "x"}, nil).Body.String(); !strings.Contains(body, "skipped") {
		t.Errorf("duplicate not reported:\n%s", body)
	}

	t.Setenv("FAKE_CALIBREDB_OUTPUT", "Another calibre program such as calibre-server or the main calibre program is running.")
	t.Setenv("FAKE_CALIBREDB_EXIT", "1")
	if body := upload(t, f.handler, map[string]string{"Dune.epub": "x"}, nil).Body.String(); !strings.Contains(body, "Another calibre program") {
		t.Errorf("calibredb failure not reported:\n%s", body)
	}

	os.Remove(f.argsLog)
	if body := upload(t, f.handler, map[string]string{"malware.exe": "x"}, nil).Body.String(); !strings.Contains(body, "Not an ebook format") {
		t.Errorf("unsupported file not rejected:\n%s", body)
	}
	if _, err := os.Stat(f.argsLog); !os.IsNotExist(err) {
		t.Error("calibredb ran for an unsupported file")
	}

	if rec := upload(t, f.handler, map[string]string{"big.epub": strings.Repeat("x", 2<<20)}, nil); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized upload: status %d", rec.Code)
	}
}

func TestUploadRejectsCrossSite(t *testing.T) {
	f := newFixture(t, nil)
	for _, h := range []http.Header{
		{"Sec-Fetch-Site": {"cross-site"}},
		{"Origin": {"https://evil.example"}},
	} {
		if rec := upload(t, f.handler, map[string]string{"a.epub": "x"}, h); rec.Code != http.StatusForbidden {
			t.Errorf("%v: status %d", h, rec.Code)
		}
	}
	if rec := upload(t, f.handler, map[string]string{"a.epub": "x"}, http.Header{"Origin": {"http://example.com"}, "Sec-Fetch-Site": {"same-origin"}}); rec.Code == http.StatusForbidden {
		t.Error("same-origin upload refused")
	}
}

func TestAuth(t *testing.T) {
	f := newFixture(t, func(c *config.Config) { c.Username, c.Password = "me", "secret" })
	for _, path := range []string{"/opds", "/lib/books/", "/lib/books/books/1/cover"} {
		if rec := f.get(t, path); rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s without credentials: status %d", path, rec.Code)
		}
	}
	req := httptest.NewRequest("GET", "/opds", nil)
	req.SetBasicAuth("me", "wrong")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: status %d", rec.Code)
	}
	req.SetBasicAuth("me", "secret")
	rec = httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("right password: status %d", rec.Code)
	}
}

func TestDiscoveredLibraries(t *testing.T) {
	dir := t.TempDir()
	testlib.NewAt(t, filepath.Join(dir, "Books"))
	testlib.NewAt(t, filepath.Join(dir, "Comics"))
	cfg := config.Config{LibrariesDir: dir}
	libs := registry(t, cfg)
	srv, err := New(cfg, libs, &calibredb.Runner{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{handler: srv.Handler()}

	root := parseFeed(t, f.get(t, "/opds"))
	if len(root.Entries) != 2 || root.Entries[1].Links[0].Href != "/opds/comics/" {
		t.Errorf("root = %+v", root.Entries)
	}
	if lib := parseFeed(t, f.get(t, "/opds/comics/")); lib.Title != "Comics" {
		t.Errorf("library feed title = %q", lib.Title)
	}

	// The dropdown lists every library and keeps the current section.
	body := f.get(t, "/lib/comics/authors").Body.String()
	for _, want := range []string{`href="/lib/books/authors"`, `href="/lib/comics/authors"`, "data-dropdown"} {
		if !strings.Contains(body, want) {
			t.Errorf("authors page lacks %s", want)
		}
	}
	if body := f.get(t, "/lib/books/books/1").Body.String(); !strings.Contains(body, `href="/lib/comics/"`) {
		t.Error("book page dropdown should link to the other library's root")
	}

	testlib.NewAt(t, filepath.Join(dir, "Manga"))
	os.RemoveAll(filepath.Join(dir, "Comics"))
	if err := libs.Refresh(); err != nil {
		t.Fatal(err)
	}
	if rec := f.get(t, "/lib/manga/"); rec.Code != http.StatusOK {
		t.Errorf("new library: status %d", rec.Code)
	}
	if rec := f.get(t, "/lib/comics/"); rec.Code != http.StatusNotFound {
		t.Errorf("removed library: status %d", rec.Code)
	}
}

func TestNoLibraries(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{LibrariesDir: dir}
	srv, err := New(cfg, registry(t, cfg), &calibredb.Runner{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{handler: srv.Handler()}
	if rec := f.get(t, "/"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "No libraries yet") {
		t.Errorf("empty state: %d\n%s", rec.Code, rec.Body)
	}
	if feed := parseFeed(t, f.get(t, "/opds")); len(feed.Entries) != 0 {
		t.Errorf("opds entries = %+v", feed.Entries)
	}
}
