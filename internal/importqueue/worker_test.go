package importqueue

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"internship-tracker/internal/jobimport"
	"internship-tracker/internal/store"
)

type fakeQueue struct {
	job          store.ImportJob
	has          bool
	finished     *jobimport.Result
	retriedAfter time.Duration
	failed       string
}

func (q *fakeQueue) ClaimImport(context.Context) (store.ImportJob, bool, error) {
	return q.job, q.has, nil
}
func (q *fakeQueue) FinishImport(_ context.Context, _ int64, r jobimport.Result) error {
	q.finished = &r
	return nil
}
func (q *fakeQueue) RetryImport(_ context.Context, _ int64, _ string, d time.Duration) error {
	q.retriedAfter = d
	return nil
}
func (q *fakeQueue) FailImport(_ context.Context, _ int64, why string) error {
	q.failed = why
	return nil
}

type fakeExtractor struct {
	r   jobimport.Result
	err error
}

func (f fakeExtractor) Extract(context.Context, string) (jobimport.Result, error) { return f.r, f.err }

func TestWorker(t *testing.T) {
	temp := fmt.Errorf("%w: timeout", jobimport.ErrTemporary)
	form := jobimport.Result{Step: jobimport.StepForm, Company: "Stripe"}
	tests := []struct {
		name     string
		attempts int
		err      error
		check    func(q *fakeQueue) bool
	}{
		{"success is ready to confirm", 1, nil, func(q *fakeQueue) bool { return q.finished != nil }},
		{"temporary failure retries with backoff", 1, temp, func(q *fakeQueue) bool { return q.retriedAfter == 30*time.Second && q.finished == nil }},
		{"third try waits longer", 3, temp, func(q *fakeQueue) bool { return q.retriedAfter == 2*time.Minute }},
		{"out of retries: the pre-filled form", MaxAttempts, temp, func(q *fakeQueue) bool { return q.finished != nil && q.finished.Company == "Stripe" }},
		{"not a usable link: failed", 1, errors.New("only http and https links are allowed"), func(q *fakeQueue) bool { return q.failed != "" }},
	}
	for _, tt := range tests {
		q := &fakeQueue{job: store.ImportJob{ID: 1, URL: "x", Attempts: tt.attempts}, has: true}
		w := &Worker{Queue: q, Extractor: fakeExtractor{r: form, err: tt.err}}
		if worked, err := w.RunOnce(context.Background()); !worked || err != nil || !tt.check(q) {
			t.Errorf("%s: worked=%v err=%v queue=%+v", tt.name, worked, err, q)
		}
	}
	if worked, _ := (&Worker{Queue: &fakeQueue{}}).RunOnce(context.Background()); worked {
		t.Error("an empty queue reported work")
	}
}
