// Package report renders diff reports with stable, deterministic
// ordering.
package report

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/guoyyit/source-structural-diff-go/internal/diff"
)

// WriteText renders rep in a stable, human-readable text format:
// changes are sorted by file, then by symbol ID, then by category.
func WriteText(w io.Writer, rep *diff.Report) error {
	changes := sorted(rep)
	for _, c := range changes {
		line := ""
		switch c.Category {
		case diff.Added:
			line = fmt.Sprintf("added      %s %s", c.New.File, c.New.ID)
		case diff.Deleted:
			line = fmt.Sprintf("deleted    %s %s", c.Old.File, c.Old.ID)
		case diff.Modified:
			line = fmt.Sprintf("modified   %s %s", c.New.File, c.New.ID)
		case diff.Unchanged:
			line = fmt.Sprintf("unchanged  %s %s", c.New.File, c.New.ID)
		case diff.Renamed, diff.Moved:
			line = fmt.Sprintf("%-10s %s %s -> %s %s (confidence %.2f%s)",
				c.Category, c.Old.File, c.Old.ID, c.New.File, c.New.ID,
				c.Confidence, candidateSuffix(c))
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
		for _, ev := range c.Evidence {
			if _, err := fmt.Fprintf(w, "  evidence: %s (weight %.2f): %s\n",
				ev.Kind, ev.Weight, ev.Detail); err != nil {
				return err
			}
		}
	}
	return nil
}

// WriteJSON renders rep as deterministic JSON (stable key and
// change ordering).
func WriteJSON(w io.Writer, rep *diff.Report) error {
	changes := sorted(rep)
	type declJSON struct {
		File string `json:"file"`
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	type evidenceJSON struct {
		Kind   string  `json:"kind"`
		Detail string  `json:"detail"`
		Weight float64 `json:"weight"`
	}
	type changeJSON struct {
		Category   string         `json:"category"`
		Old        *declJSON      `json:"old,omitempty"`
		New        *declJSON      `json:"new,omitempty"`
		Confidence float64        `json:"confidence"`
		Candidate  bool           `json:"candidate,omitempty"`
		Evidence   []evidenceJSON `json:"evidence,omitempty"`
	}
	toDecl := func(c diff.Change, old bool) *declJSON {
		d := c.New
		if old {
			d = c.Old
		}
		if d == nil {
			return nil
		}
		return &declJSON{File: d.File, ID: d.ID, Kind: string(d.Kind)}
	}
	out := make([]changeJSON, 0, len(changes))
	for _, c := range changes {
		cj := changeJSON{
			Category:   string(c.Category),
			Old:        toDecl(c, true),
			New:        toDecl(c, false),
			Confidence: c.Confidence,
			Candidate:  c.Candidate,
		}
		for _, ev := range c.Evidence {
			cj.Evidence = append(cj.Evidence, evidenceJSON{
				Kind: ev.Kind, Detail: ev.Detail, Weight: ev.Weight,
			})
		}
		out = append(out, cj)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// sorted returns a copy of the report's changes in stable output
// order (file, symbol ID, category).
func sorted(rep *diff.Report) []diff.Change {
	changes := make([]diff.Change, len(rep.Changes))
	copy(changes, rep.Changes)
	diff.SortChanges(changes)
	return changes
}

func candidateSuffix(c diff.Change) string {
	if c.Candidate {
		return ", candidate"
	}
	return ""
}
