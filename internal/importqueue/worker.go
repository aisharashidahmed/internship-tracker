// Package importqueue works the queue of pasted job links in the
// background, so pasting never makes the page wait for other websites.
package importqueue

import (
	"context"
	"errors"
	"log"
	"time"

	"internship-tracker/internal/jobimport"
	"internship-tracker/internal/store"
)

// MaxAttempts: after this many tries a link stops waiting for a site to
// recover and goes to the form step with whatever we know.
const MaxAttempts = 4

// Backoff before retry n (1-based): 30s, 1m, 2m.
func Backoff(attempt int) time.Duration { return 30 * time.Second << (attempt - 1) }

// Queue is what the worker needs from the database.
type Queue interface {
	ClaimImport(ctx context.Context) (store.ImportJob, bool, error)
	FinishImport(ctx context.Context, id int64, r jobimport.Result) error
	RetryImport(ctx context.Context, id int64, why string, delay time.Duration) error
	FailImport(ctx context.Context, id int64, why string) error
}

// Worker claims links one at a time and runs the extraction chain.
type Worker struct {
	Queue     Queue
	Extractor interface {
		Extract(ctx context.Context, rawURL string) (jobimport.Result, error)
	}
}

// RunOnce handles one due link. worked is false when the queue was empty.
func (w *Worker) RunOnce(ctx context.Context) (worked bool, err error) {
	job, ok, err := w.Queue.ClaimImport(ctx)
	if err != nil || !ok {
		return false, err
	}
	r, err := w.Extractor.Extract(ctx, job.URL)
	switch {
	case err == nil:
		return true, w.Queue.FinishImport(ctx, job.ID, r)
	case errors.Is(err, jobimport.ErrTemporary) && job.Attempts < MaxAttempts:
		return true, w.Queue.RetryImport(ctx, job.ID, err.Error(), Backoff(job.Attempts))
	case errors.Is(err, jobimport.ErrTemporary):
		return true, w.Queue.FinishImport(ctx, job.ID, r) // out of retries: the form, pre-filled
	default:
		return true, w.Queue.FailImport(ctx, job.ID, err.Error()) // not a usable link at all
	}
}

// Run works the queue until ctx is cancelled: back to back while there is
// work, checking every poll interval when there isn't.
func (w *Worker) Run(ctx context.Context, poll time.Duration) {
	for ctx.Err() == nil {
		worked, err := w.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("import queue: %v", err)
		}
		if !worked || err != nil {
			select {
			case <-ctx.Done():
			case <-time.After(poll):
			}
		}
	}
}
