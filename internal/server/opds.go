package server

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"calibruh/internal/library"
)

// OPDS 1.2: https://specs.opds.io/opds-1.2

const (
	opdsPageSize = 50
	navType      = "application/atom+xml;profile=opds-catalog;kind=navigation"
	acqType      = "application/atom+xml;profile=opds-catalog;kind=acquisition"
)

type feed struct {
	XMLName      xml.Name `xml:"feed"`
	Xmlns        string   `xml:"xmlns,attr"`
	XmlnsDC      string   `xml:"xmlns:dc,attr"`
	XmlnsOPDS    string   `xml:"xmlns:opds,attr"`
	XmlnsOS      string   `xml:"xmlns:opensearch,attr"`
	ID           string   `xml:"id"`
	Title        string   `xml:"title"`
	Updated      string   `xml:"updated"`
	Links        []link   `xml:"link"`
	TotalResults int      `xml:"opensearch:totalResults,omitempty"`
	ItemsPerPage int      `xml:"opensearch:itemsPerPage,omitempty"`
	StartIndex   int      `xml:"opensearch:startIndex,omitempty"`
	Entries      []entry  `xml:"entry"`
}

type link struct {
	Rel   string `xml:"rel,attr,omitempty"`
	Href  string `xml:"href,attr"`
	Type  string `xml:"type,attr,omitempty"`
	Title string `xml:"title,attr,omitempty"`
}

type entry struct {
	Title      string         `xml:"title"`
	ID         string         `xml:"id"`
	Updated    string         `xml:"updated"`
	Authors    []person       `xml:"author,omitempty"`
	Languages  []string       `xml:"dc:language,omitempty"`
	Issued     string         `xml:"dc:issued,omitempty"`
	Publisher  string         `xml:"dc:publisher,omitempty"`
	Categories []atomCategory `xml:"category,omitempty"`
	Content    *text          `xml:"content,omitempty"`
	Links      []link         `xml:"link"`
}

type person struct {
	Name string `xml:"name"`
}

type atomCategory struct {
	Term  string `xml:"term,attr"`
	Label string `xml:"label,attr"`
}

type text struct {
	Type string `xml:"type,attr"`
	Body string `xml:",chardata"`
}

func newFeed(id, title string, links ...link) *feed {
	return &feed{
		Xmlns:     "http://www.w3.org/2005/Atom",
		XmlnsDC:   "http://purl.org/dc/terms/",
		XmlnsOPDS: "http://opds-spec.org/2010/catalog",
		XmlnsOS:   "http://a9.com/-/spec/opensearch/1.1/",
		ID:        id,
		Title:     title,
		Updated:   time.Now().UTC().Format(time.RFC3339),
		Links:     links,
	}
}

func navEntry(id, title, href, typ, summary string) entry {
	e := entry{Title: title, ID: id, Updated: time.Now().UTC().Format(time.RFC3339), Links: []link{{Rel: "subsection", Href: href, Type: typ}}}
	if summary != "" {
		e.Content = &text{Type: "text", Body: summary}
	}
	return e
}

func writeXML(w http.ResponseWriter, contentType string, v any) {
	w.Header().Set("Content-Type", contentType+"; charset=utf-8")
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(v)
}

func (s *Server) opdsRoot(w http.ResponseWriter, r *http.Request) {
	libs := s.libs.List()
	if len(libs) == 1 {
		writeXML(w, navType, s.libraryFeed(libs[0], "/opds"))
		return
	}
	f := newFeed("calibruh:root", "calibruh",
		link{Rel: "self", Href: "/opds", Type: navType},
		link{Rel: "start", Href: "/opds", Type: navType})
	for _, l := range libs {
		f.Entries = append(f.Entries, navEntry("calibruh:"+l.Slug, l.Name, "/opds/"+l.Slug+"/", navType, ""))
	}
	writeXML(w, navType, f)
}

func (s *Server) opdsLibrary(w http.ResponseWriter, r *http.Request) {
	lib := s.library(w, r)
	if lib == nil {
		return
	}
	writeXML(w, navType, s.libraryFeed(lib, "/opds/"+lib.Slug+"/"))
}

func (s *Server) libraryFeed(lib *library.Library, self string) *feed {
	base := "/opds/" + lib.Slug
	f := newFeed("calibruh:"+lib.Slug, lib.Name, s.commonLinks(lib, self, navType)...)
	f.Entries = []entry{
		navEntry("calibruh:"+lib.Slug+":new", "Newest", base+"/new", acqType, "Recently added books"),
		navEntry("calibruh:"+lib.Slug+":titles", "All titles", base+"/titles", acqType, "Every book, by title"),
	}
	for _, c := range categories {
		f.Entries = append(f.Entries, navEntry("calibruh:"+lib.Slug+":"+c.Path, c.Label, base+"/"+c.Path, navType, "Browse by "+strings.ToLower(c.Label)))
	}
	return f
}

func (s *Server) commonLinks(lib *library.Library, self, selfType string) []link {
	start := "/opds"
	if len(s.libs.List()) > 1 {
		start = "/opds/" + lib.Slug + "/"
	}
	return []link{
		{Rel: "self", Href: self, Type: selfType},
		{Rel: "start", Href: start, Type: navType},
		{Rel: "search", Href: "/opds/" + lib.Slug + "/opensearch.xml", Type: "application/opensearchdescription+xml"},
		{Rel: "search", Href: "/opds/" + lib.Slug + "/search?q={searchTerms}", Type: "application/atom+xml"},
	}
}

func (s *Server) opdsOpenSearch(w http.ResponseWriter, r *http.Request) {
	lib := s.library(w, r)
	if lib == nil {
		return
	}
	type urlTemplate struct {
		Type     string `xml:"type,attr"`
		Template string `xml:"template,attr"`
	}
	writeXML(w, "application/opensearchdescription+xml", struct {
		XMLName     xml.Name    `xml:"http://a9.com/-/spec/opensearch/1.1/ OpenSearchDescription"`
		ShortName   string      `xml:"ShortName"`
		Description string      `xml:"Description"`
		URL         urlTemplate `xml:"Url"`
	}{
		ShortName:   lib.Name,
		Description: "Search " + lib.Name,
		URL:         urlTemplate{Type: acqType, Template: "/opds/" + lib.Slug + "/search?q={searchTerms}"},
	})
}

func (s *Server) opdsBooks(title string, sort library.Sort) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lib := s.library(w, r)
		if lib == nil {
			return
		}
		s.writeAcquisition(w, r, lib, title, library.Query{Sort: sort})
	}
}

func (s *Server) opdsSearch(w http.ResponseWriter, r *http.Request) {
	lib := s.library(w, r)
	if lib == nil {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	s.writeAcquisition(w, r, lib, fmt.Sprintf("Search: %s", q), library.Query{Search: q, Sort: library.SortTitle})
}

func (s *Server) opdsCategories(c category) http.HandlerFunc {
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
		f := newFeed("calibruh:"+lib.Slug+":"+c.Path, c.Label, s.commonLinks(lib, r.URL.Path, navType)...)
		for _, it := range items {
			id := strconv.FormatInt(it.ID, 10)
			books := "1 book"
			if it.Count != 1 {
				books = fmt.Sprintf("%d books", it.Count)
			}
			f.Entries = append(f.Entries, navEntry("calibruh:"+lib.Slug+":"+c.Param+":"+id, it.Name, r.URL.Path+"/"+id, acqType, books))
		}
		writeXML(w, navType, f)
	}
}

func (s *Server) opdsCategoryBooks(c category) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lib := s.library(w, r)
		if lib == nil {
			return
		}
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		name, err := lib.CategoryName(r.Context(), c.Kind, id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		q := library.Query{Filter: c.Kind, FilterID: id}
		if c.Kind != library.KindSeries {
			q.Sort = library.SortTitle
		}
		s.writeAcquisition(w, r, lib, name, q)
	}
}

func (s *Server) writeAcquisition(w http.ResponseWriter, r *http.Request, lib *library.Library, title string, q library.Query) {
	page := pageParam(r)
	q.Limit, q.Offset = opdsPageSize, (page-1)*opdsPageSize
	books, total, err := lib.Books(r.Context(), q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	self := withQuery(r)
	f := newFeed("calibruh:"+lib.Slug+":"+self, title, s.commonLinks(lib, self, acqType)...)
	f.TotalResults, f.ItemsPerPage, f.StartIndex = total, opdsPageSize, q.Offset+1
	if page > 1 {
		f.Links = append(f.Links,
			link{Rel: "first", Href: withQuery(r, "page", ""), Type: acqType},
			link{Rel: "previous", Href: withQuery(r, "page", strconv.Itoa(page-1)), Type: acqType})
	}
	if q.Offset+len(books) < total {
		f.Links = append(f.Links, link{Rel: "next", Href: withQuery(r, "page", strconv.Itoa(page+1)), Type: acqType})
	}
	for _, b := range books {
		f.Entries = append(f.Entries, bookEntry(lib, b))
	}
	writeXML(w, acqType, f)
}

func bookEntry(lib *library.Library, b library.Book) entry {
	base := fmt.Sprintf("/lib/%s/books/%d", lib.Slug, b.ID)
	id := "urn:uuid:" + b.UUID
	if b.UUID == "" {
		id = fmt.Sprintf("calibruh:%s:book:%d", lib.Slug, b.ID)
	}
	updated := b.Modified
	if updated.IsZero() {
		updated = b.Added
	}
	e := entry{Title: b.Title, ID: id, Updated: updated.UTC().Format(time.RFC3339)}
	for _, a := range b.Authors {
		e.Authors = append(e.Authors, person{a})
	}
	if !b.Published.IsZero() {
		e.Issued = b.Published.Format("2006-01-02")
	}
	if b.Series != "" {
		e.Content = &text{Type: "text", Body: fmt.Sprintf("Book %s of %s", strconv.FormatFloat(b.SeriesIndex, 'f', -1, 64), b.Series)}
	}
	if b.HasCover {
		e.Links = append(e.Links,
			link{Rel: "http://opds-spec.org/image", Href: base + "/cover", Type: "image/jpeg"},
			link{Rel: "http://opds-spec.org/image/thumbnail", Href: base + "/cover", Type: "image/jpeg"})
	}
	for _, f := range b.Formats {
		format := strings.ToLower(f)
		e.Links = append(e.Links, link{Rel: "http://opds-spec.org/acquisition", Href: base + "/file/" + format, Type: mimeType(format), Title: f})
	}
	return e
}
