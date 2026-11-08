// Package source defines what a source adapter is. Each subpackage
// (simplify, later speedyapply) implements Adapter for one GitHub repo.
//
// Adapters only translate bytes into postings. They don't download anything
// and don't touch the database; package ingest does both.
package source

import "internship-tracker/internal/listing"

// Adapter turns one source's raw file into the common Posting shape.
type Adapter interface {
	// Name matches sources.name in the database, e.g. "simplify".
	Name() string
	// Parse converts the raw file body into postings, one per season.
	Parse(body []byte) ([]listing.Posting, error)
}
