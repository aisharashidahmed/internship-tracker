package source

import (
	"internship-tracker/internal/source/simplify"
	"internship-tracker/internal/source/speedyapply"
)

// All returns every source, in the order ingestion runs them. Simplify
// goes first: it has explicit seasons, degrees and closed flags, so its
// listings are the better base for SpeedyApply postings to join.
func All() []Adapter {
	return []Adapter{
		simplify.Adapter{},
		speedyapply.Adapter{},
	}
}
