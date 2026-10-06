// Package export downloads the MODS of a resolved container's members
// concurrently and writes one <noid>.xml per member. It's the v2 analogue of
// the v1 script's `xargs -P4 wget` fan-out: a bounded worker pool, best-effort
// (a member that fails is recorded and skipped, never fatal).
package export

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/NEU-Libraries/hyperion/internal/atlas"
)

const maxConcurrency = 16

// Progress is emitted once per finished member so a caller can drive a bar.
// Err is nil on success.
type Progress struct {
	NOID string
	Err  error
}

// Result is the tally returned when the run completes.
type Result struct {
	Saved  []string
	Failed map[string]error
}

// Download fetches each member's MODS and writes it to dir/<noid>.xml using a
// pool of `concurrency` workers (clamped to [1, 16]). If progress is non-nil, a
// Progress is sent on it per finished member; the caller must drain it (the TUI
// does; the headless path passes nil). The returned Result tallies the run.
func Download(ctx context.Context, client *atlas.Client, members []atlas.Member, dir string, concurrency int, progress chan<- Progress) (Result, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, err
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > maxConcurrency {
		concurrency = maxConcurrency
	}

	jobs := make(chan atlas.Member)
	result := Result{Failed: make(map[string]error)}
	var mu sync.Mutex
	var wg sync.WaitGroup

	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := range jobs {
				err := fetch(ctx, client, m.NOID, dir)
				mu.Lock()
				if err != nil {
					result.Failed[m.NOID] = err
				} else {
					result.Saved = append(result.Saved, m.NOID)
				}
				mu.Unlock()
				if progress != nil {
					progress <- Progress{NOID: m.NOID, Err: err}
				}
			}
		}()
	}

	for _, m := range members {
		select {
		case <-ctx.Done():
			// Stop feeding on cancellation; workers drain and exit.
		case jobs <- m:
			continue
		}
		break
	}
	close(jobs)
	wg.Wait()
	return result, ctx.Err()
}

func fetch(ctx context.Context, client *atlas.Client, noid, dir string) error {
	xml, err := client.MODS(ctx, noid)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, noid+".xml"), xml, 0o644)
}

// Existing counts the members whose <noid>.xml is already in dir — the files
// a Download into dir would replace.
func Existing(dir string, members []atlas.Member) int {
	n := 0
	for _, m := range members {
		if _, err := os.Stat(filepath.Join(dir, m.NOID+".xml")); err == nil {
			n++
		}
	}
	return n
}
