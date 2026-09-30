// Package diff computes structural differences between two declaration
// trees and classifies each change.
package diff

import (
	"sort"

	"github.com/guoyyit/source-structural-diff-go/internal/decl"
)

// Category is the classification of a single declaration-level change.
type Category string

const (
	Unchanged Category = "unchanged"
	Modified  Category = "modified"
	Added     Category = "added"
	Deleted   Category = "deleted"
	Renamed   Category = "renamed"
	Moved     Category = "moved"
)

// Evidence is one structural-similarity observation supporting a
// rename/move classification.
type Evidence struct {
	// Kind names the evidence source, e.g. "identical-signature",
	// "identical-body", "name-similarity", "body-similarity".
	Kind string
	// Detail is a human-readable explanation.
	Detail string
	// Weight is this evidence's contribution to the confidence score.
	Weight float64
}

// Change describes one declaration-level difference between an old
// (base) and new (target) tree.
type Change struct {
	Category Category

	// Old is the declaration in the base tree; nil for Added.
	Old *decl.Decl
	// New is the declaration in the target tree; nil for Deleted.
	New *decl.Decl

	// Confidence in [0,1] for Renamed/Moved; 1 for exact matches.
	Confidence float64
	// Evidence lists the structural observations behind Confidence.
	Evidence []Evidence

	// Candidate is true when similarity evidence exists but is below
	// the acceptance threshold: the pair is reported as a candidate
	// match instead of being silently misclassified.
	Candidate bool
}

// Report is the full diff between two trees.
type Report struct {
	Changes []Change
}

// Options tunes classification thresholds.
type Options struct {
	// AcceptThreshold is the minimum confidence to classify a pair as
	// Renamed or Moved. Default 0.75.
	AcceptThreshold float64
	// CandidateThreshold is the minimum confidence to report a pair as
	// a candidate match. Default 0.35.
	CandidateThreshold float64
}

func (o Options) withDefaults() Options {
	if o.AcceptThreshold <= 0 {
		o.AcceptThreshold = 0.75
	}
	if o.CandidateThreshold <= 0 {
		o.CandidateThreshold = 0.35
	}
	return o
}

// key identifies a declaration within a tree.
type key struct {
	File string
	ID   string
}

func keyOf(d *decl.Decl) key { return key{File: d.File, ID: d.ID} }

// Compare computes the structural diff from oldTree to newTree.
// The result is deterministic: identical inputs always produce
// identical reports.
func Compare(oldTree, newTree *decl.Tree, opts Options) *Report {
	opts = opts.withDefaults()
	oldDecls := flatten(oldTree)
	newDecls := flatten(newTree)

	oldByKey := make(map[key]*decl.Decl, len(oldDecls))
	for _, d := range oldDecls {
		oldByKey[keyOf(d)] = d
	}
	newByKey := make(map[key]*decl.Decl, len(newDecls))
	for _, d := range newDecls {
		newByKey[keyOf(d)] = d
	}

	rep := &Report{}
	matchedOld := make(map[key]bool)
	matchedNew := make(map[key]bool)

	// Phase 1: declarations present under the same file+ID in both
	// trees are Unchanged or Modified.
	for _, od := range oldDecls {
		k := keyOf(od)
		nd, ok := newByKey[k]
		if !ok {
			continue
		}
		matchedOld[k] = true
		matchedNew[k] = true
		if od.BodyHash == nd.BodyHash && od.Signature == nd.Signature {
			rep.Changes = append(rep.Changes, Change{
				Category:   Unchanged,
				Old:        od,
				New:        nd,
				Confidence: 1,
			})
		} else {
			rep.Changes = append(rep.Changes, Change{
				Category:   Modified,
				Old:        od,
				New:        nd,
				Confidence: 1,
			})
		}
	}

	// Phase 2: match remaining declarations across names and files
	// using structural similarity evidence.
	var remOld, remNew []*decl.Decl
	for _, d := range oldDecls {
		if !matchedOld[keyOf(d)] {
			remOld = append(remOld, d)
		}
	}
	for _, d := range newDecls {
		if !matchedNew[keyOf(d)] {
			remNew = append(remNew, d)
		}
	}

	type pair struct {
		old, new *decl.Decl
		conf     float64
		ev       []Evidence
	}
	var pairs []pair
	for _, cand := range candidatePairs(remOld, remNew) {
		conf, ev := similarity(cand.old, cand.new)
		if conf >= opts.CandidateThreshold {
			pairs = append(pairs, pair{cand.old, cand.new, conf, ev})
		}
	}
	// Deterministic greedy assignment: highest confidence first,
	// ties broken by stable keys.
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].conf != pairs[j].conf {
			return pairs[i].conf > pairs[j].conf
		}
		oi, oj := keyOf(pairs[i].old), keyOf(pairs[j].old)
		if oi != oj {
			return oi.File < oj.File || (oi.File == oj.File && oi.ID < oj.ID)
		}
		ni, nj := keyOf(pairs[i].new), keyOf(pairs[j].new)
		return ni.File < nj.File || (ni.File == nj.File && ni.ID < nj.ID)
	})

	usedOld := make(map[key]bool)
	usedNew := make(map[key]bool)
	for _, p := range pairs {
		ok, nk := keyOf(p.old), keyOf(p.new)
		if usedOld[ok] || usedNew[nk] {
			continue
		}
		usedOld[ok] = true
		usedNew[nk] = true
		cat := Renamed
		if p.old.File != p.new.File {
			cat = Moved
		}
		rep.Changes = append(rep.Changes, Change{
			Category:   cat,
			Old:        p.old,
			New:        p.new,
			Confidence: p.conf,
			Evidence:   p.ev,
			Candidate:  p.conf < opts.AcceptThreshold,
		})
	}

	// Phase 3: whatever remains is Deleted or Added.
	for _, d := range remOld {
		if !usedOld[keyOf(d)] {
			rep.Changes = append(rep.Changes, Change{
				Category:   Deleted,
				Old:        d,
				Confidence: 1,
			})
		}
	}
	for _, d := range remNew {
		if !usedNew[keyOf(d)] {
			rep.Changes = append(rep.Changes, Change{
				Category:   Added,
				New:        d,
				Confidence: 1,
			})
		}
	}

	SortChanges(rep.Changes)
	return rep
}

// SortChanges orders changes deterministically: by file, then symbol
// ID, then category. The file used is the new declaration's file when
// present, else the old one.
func SortChanges(changes []Change) {
	sort.SliceStable(changes, func(i, j int) bool {
		fi, fj := changeFile(changes[i]), changeFile(changes[j])
		if fi != fj {
			return fi < fj
		}
		si, sj := changeID(changes[i]), changeID(changes[j])
		if si != sj {
			return si < sj
		}
		return changes[i].Category < changes[j].Category
	})
}

func changeFile(c Change) string {
	if c.New != nil {
		return c.New.File
	}
	return c.Old.File
}

func changeID(c Change) string {
	if c.New != nil {
		return c.New.ID
	}
	return c.Old.ID
}

func flatten(t *decl.Tree) []*decl.Decl {
	if t == nil {
		return nil
	}
	paths := make([]string, 0, len(t.Files))
	for p := range t.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var out []*decl.Decl
	for _, p := range paths {
		f := t.Files[p]
		for i := range f.Decls {
			out = append(out, &f.Decls[i])
		}
	}
	return out
}

// candidatePair is a potentially matching old/new declaration.
type candidatePair struct {
	old, new *decl.Decl
}

// candidatePairs generates plausible rename/move pairs without an
// O(n²) blow-up: only declarations sharing a kind plus at least one
// strong structural index (body hash, signature, or name) are scored.
func candidatePairs(remOld, remNew []*decl.Decl) []candidatePair {
	byBody := make(map[string][]*decl.Decl)
	bySig := make(map[string][]*decl.Decl)
	byName := make(map[string][]*decl.Decl)
	for _, nd := range remNew {
		byBody[string(nd.Kind)+"|"+nd.BodyHash] = append(byBody[string(nd.Kind)+"|"+nd.BodyHash], nd)
		bySig[string(nd.Kind)+"|"+nd.Signature] = append(bySig[string(nd.Kind)+"|"+nd.Signature], nd)
		byName[string(nd.Kind)+"|"+nd.Name] = append(byName[string(nd.Kind)+"|"+nd.Name], nd)
	}
	seen := make(map[[2]key]bool)
	var out []candidatePair
	add := func(od, nd *decl.Decl) {
		k := [2]key{keyOf(od), keyOf(nd)}
		if !seen[k] {
			seen[k] = true
			out = append(out, candidatePair{od, nd})
		}
	}
	for _, od := range remOld {
		for _, nd := range byBody[string(od.Kind)+"|"+od.BodyHash] {
			add(od, nd)
		}
		for _, nd := range bySig[string(od.Kind)+"|"+od.Signature] {
			add(od, nd)
		}
		for _, nd := range byName[string(od.Kind)+"|"+od.Name] {
			add(od, nd)
		}
	}
	return out
}
