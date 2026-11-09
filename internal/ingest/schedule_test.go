package ingest

import (
	"context"
	"errors"
	"testing"
	"time"

	"internship-tracker/internal/store"
)

func TestEvery_RunsImmediatelyThenRepeatsUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runs := make(chan time.Time, 10)
	done := make(chan struct{})
	start := time.Now()

	go func() {
		Every(ctx, 20*time.Millisecond, func(context.Context) { runs <- time.Now() })
		close(done)
	}()

	first := <-runs
	if first.Sub(start) > 200*time.Millisecond { // generous: the interval is 20ms, so 200ms means it waited
		t.Errorf("first run after %s; want immediately", first.Sub(start))
	}
	<-runs // the scheduler keeps going
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Every did not stop after the context was cancelled")
	}
}

func TestSummary(t *testing.T) {
	ms := 81 * time.Millisecond
	tests := []struct {
		r    Result
		want string
	}{
		{Result{Report: Report{Source: "simplify", NotModified: true, Took: ms}},
			"simplify: unchanged (HTTP 304) in 81ms"},
		{Result{Report: Report{Source: "speedyapply", Postings: 827, Took: ms,
			Counts: map[store.Outcome]int{store.OutcomeNew: 3, store.OutcomeStale: 1}}},
			"speedyapply: 827 postings, 3 new, 0 updated, 0 unchanged, 1 stale, 0 closed in 81ms"},
		{Result{Report: Report{Source: "speedyapply"}, Err: errors.New("unexpected status 404")},
			"speedyapply: FAILED: unexpected status 404"},
	}
	for _, tt := range tests {
		if got := Summary(tt.r); got != tt.want {
			t.Errorf("Summary = %q\n want    %q", got, tt.want)
		}
	}
}
