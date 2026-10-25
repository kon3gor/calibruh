package calibredb

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func fake(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "calibredb")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAddParsesIDs(t *testing.T) {
	r := &Runner{Bin: fake(t, `echo "Some warning"; echo "Added book ids: 7, 8"`)}
	res, err := r.Add(context.Background(), "/lib", "a.epub", "b.epub")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.IDs, []int64{7, 8}) || res.Duplicate {
		t.Errorf("result = %+v", res)
	}
}

func TestAddDuplicateFlag(t *testing.T) {
	r := &Runner{Bin: fake(t, `echo "$@"`), Duplicates: true}
	res, err := r.Add(context.Background(), "/lib", "a.epub")
	if err != nil {
		t.Fatal(err)
	}
	if want := "add --with-library /lib --duplicates -- a.epub\n"; res.Output != want {
		t.Errorf("args = %q, want %q", res.Output, want)
	}
}

func TestAddFailure(t *testing.T) {
	r := &Runner{Bin: fake(t, `echo "first"; echo "Another calibre program is running" >&2; exit 1`)}
	_, err := r.Add(context.Background(), "/lib", "a.epub")
	if err == nil || err.Error() != "calibredb add: exit status 1: Another calibre program is running" {
		t.Errorf("err = %v", err)
	}
}

func TestAddTimeout(t *testing.T) {
	r := &Runner{Bin: fake(t, `sleep 5`), Timeout: 100 * time.Millisecond}
	start := time.Now()
	if _, err := r.Add(context.Background(), "/lib", "a.epub"); err == nil {
		t.Error("expected a timeout error")
	}
	if time.Since(start) > 3*time.Second {
		t.Error("timeout did not stop calibredb")
	}
}
