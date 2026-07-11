package ingest

import "github.com/freeeve/libcat/similar"

// SimilarWork converts an admin-side summary into the similarity scorer's input
// . It is one of exactly two converters; project.Work.SimilarWork is
// the other, and a test drives both from the same graph and requires equal
// results, because an OPAC rail and an admin panel that disagree about what a
// Work resembles is a bug the reader can see and nobody can explain.
//
// Suppressed Works are kept. Suppression hides a Work from the public projection,
// which never reaches the scorer at all; the admin surface shows suppressed Works
// and so must recommend them. Tombstoned Works are passed through with the flag
// set and excluded by similar.Build -- retiring a record must not leave it
// recommended from elsewhere.
//
// Held collapses the two digital/physical signals the summary carries separately
// into the one predicate project.Work.Held already publishes.
func (s WorkSummary) SimilarWork() similar.Work {
	return similar.Work{
		WorkID:       s.WorkID,
		Tombstoned:   s.Tombstoned,
		Held:         s.HasAvailability || s.Items > 0,
		Series:       s.Series,
		Contributors: s.Contributors,
		Tags:         s.Tags,
		Subjects:     s.Subjects,
		Languages:    s.Languages,
	}
}

// SimilarWorks converts a batch, preserving order.
func SimilarWorks(summaries []WorkSummary) []similar.Work {
	out := make([]similar.Work, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, s.SimilarWork())
	}
	return out
}
