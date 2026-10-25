// Package libraries keeps the set of served libraries: the ones listed in the
// config plus any found in a watched directory, which may come and go while
// calibruh runs.
package libraries

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"calibruh/internal/config"
	"calibruh/internal/library"
)

type Registry struct {
	dir    string
	static []*library.Library

	refreshMu sync.Mutex
	// found holds every discovered library that is open, keyed by root,
	// including ones not served because their slug is taken.
	found map[string]*library.Library
	// problems remembers the last warning per root so each is logged once.
	problems map[string]string

	mu     sync.RWMutex
	libs   []*library.Library
	bySlug map[string]*library.Library
}

// New opens the configured libraries and, if dir is set, scans it once.
func New(static []config.Library, dir string) (*Registry, error) {
	r := &Registry{dir: dir, found: map[string]*library.Library{}, problems: map[string]string{}}
	slugs := map[string]string{}
	for _, lc := range static {
		l, err := library.Open(lc.Name, lc.Path)
		if err != nil {
			r.Close()
			return nil, err
		}
		r.static = append(r.static, l)
		slog.Info("library opened", "name", l.Name, "path", l.Root, "url", "/lib/"+l.Slug+"/")
		if prev, dup := slugs[l.Slug]; dup {
			r.Close()
			return nil, fmt.Errorf("libraries %q and %q have the same URL slug %q", prev, l.Name, l.Slug)
		}
		slugs[l.Slug] = l.Name
	}
	if dir != "" {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			r.Close()
			return nil, fmt.Errorf("libraries_dir %q is not a directory", dir)
		}
	}
	if err := r.Refresh(); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

// Refresh rescans the directory: subfolders with a metadata.db become
// libraries named after the folder, and libraries whose folder is gone are
// closed. Configured libraries always come first and win slug clashes.
func (r *Registry) Refresh() error {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()

	var names map[string]string
	if r.dir != "" {
		var err error
		if names, err = r.scan(); err != nil {
			// Keep serving what we have rather than dropping every
			// library over a transient error.
			return err
		}
	}
	for _, l := range r.static {
		delete(names, l.Root)
	}

	found := map[string]*library.Library{}
	for root, name := range names {
		if l := r.found[root]; l != nil {
			found[root] = l
			continue
		}
		l, err := library.Open(name, root)
		if err != nil {
			r.warn(root, err.Error())
			continue
		}
		slog.Info("library added", "name", l.Name, "path", l.Root, "url", "/lib/"+l.Slug+"/")
		found[root] = l
	}
	for root := range r.problems {
		if _, ok := names[root]; !ok {
			delete(r.problems, root)
		}
	}

	discovered := make([]*library.Library, 0, len(found))
	for _, l := range found {
		discovered = append(discovered, l)
	}
	slices.SortFunc(discovered, func(a, b *library.Library) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.Root, b.Root))
	})

	libs := slices.Clone(r.static)
	bySlug := map[string]*library.Library{}
	for _, l := range r.static {
		bySlug[l.Slug] = l
	}
	for _, l := range discovered {
		if prev := bySlug[l.Slug]; prev != nil {
			r.warn(l.Root, fmt.Sprintf("not served: URL /lib/%s/ is taken by %s", l.Slug, prev.Root))
			continue
		}
		delete(r.problems, l.Root)
		bySlug[l.Slug] = l
		libs = append(libs, l)
	}

	var removed []*library.Library
	for root, l := range r.found {
		if found[root] == nil {
			removed = append(removed, l)
		}
	}

	r.mu.Lock()
	r.libs, r.bySlug = libs, bySlug
	r.mu.Unlock()
	r.found = found

	// sql.DB.Close waits for queries already running, so requests that
	// are mid-flight on a removed library finish first.
	for _, l := range removed {
		slog.Info("library removed", "name", l.Name, "path", l.Root)
		l.Close()
	}
	return nil
}

// scan maps the root of every library directly inside dir to its name.
func (r *Registry) scan() (map[string]string, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		root, err := filepath.Abs(filepath.Join(r.dir, e.Name()))
		if err != nil {
			continue
		}
		// Stat rather than e.IsDir() so symlinked libraries count.
		if info, err := os.Stat(filepath.Join(root, "metadata.db")); err == nil && info.Mode().IsRegular() {
			names[root] = e.Name()
		}
	}
	return names, nil
}

func (r *Registry) warn(root, problem string) {
	if r.problems[root] != problem {
		slog.Warn("library skipped", "path", root, "reason", problem)
		r.problems[root] = problem
	}
}

// Watch refreshes every interval until ctx is done.
func (r *Registry) Watch(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	var lastErr string
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			err := r.Refresh()
			if msg := fmt.Sprint(err); err != nil && msg != lastErr {
				slog.Warn("scanning libraries failed", "dir", r.dir, "err", err)
				lastErr = msg
			} else if err == nil {
				lastErr = ""
			}
		}
	}
}

// List returns the served libraries: configured ones first, then
// discovered ones by name.
func (r *Registry) List() []*library.Library {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.libs)
}

func (r *Registry) Get(slug string) *library.Library {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.bySlug[slug]
}

func (r *Registry) Close() {
	for _, l := range r.static {
		l.Close()
	}
	for _, l := range r.found {
		l.Close()
	}
}
