// Package calibredb adds books to a library by running Calibre's calibredb.
package calibredb

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Runner struct {
	Bin        string
	Duplicates bool
	Timeout    time.Duration

	// calibredb refuses to run while another calibre program holds a
	// library, so concurrent calls would fail anyway.
	mu sync.Mutex
}

type Result struct {
	IDs []int64
	// Duplicate is set when calibredb skipped the file because a book with
	// the same title and authors already exists.
	Duplicate bool
	Output    string
}

var addedRe = regexp.MustCompile(`(?m)^Added book ids?: ([\d, ]+)`)

func (r *Runner) Add(ctx context.Context, library string, files ...string) (Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	args := []string{"add", "--with-library", library}
	if r.Duplicates {
		args = append(args, "--duplicates")
	}
	args = append(append(args, "--"), files...)

	cmd := exec.CommandContext(ctx, r.Bin, args...)
	killGroup(cmd)
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	res := Result{Output: string(out), Duplicate: strings.Contains(string(out), "already exist in the database")}
	if m := addedRe.FindStringSubmatch(res.Output); m != nil {
		for _, s := range strings.Split(m[1], ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				res.IDs = append(res.IDs, id)
			}
		}
	}
	if err != nil {
		return res, fmt.Errorf("calibredb add: %w: %s", err, lastLine(res.Output))
	}
	return res, nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
