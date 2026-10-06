// Package diff computes structural differences between two tree models.
package diff

import (
	"fmt"
	"math"
	"sort"

	"github.com/guoyyit/source-structural-diff-go/internal/model"
)

// Category is the classification of a single diff entry.
type Category string

const (
	Unchanged Category = "unchanged"
	Modified  Category = "modified"
	Added     Category = "added"
	Deleted   Category = "deleted"
	Renamed   Category = "renamed"
	Moved     Category = "moved"
)

// Evidence is one piece of structural-similarity evidence.
type Evidence struct {
	Kind   string  `json:"kind"`
	Detail string  `json:"detail"`
	Score  float64 `json:"score"`
}

// Entry is one classified difference (or match candidate).
type Entry struct {
	Category   Category   `json:"category"`
	Kind       model.Kind `json:"kind"`
	Name       string     `json:"name"`
	OldName    string     `json:"oldName,omitempty"`
	File       string     `json:"file"`
	OldFile    string     `json:"oldFile,omitempty"`
	Confidence float64    `json:"confidence,omitempty"`
	Evidence   []Evidence `json:"evidence,omitempty"`
	Candidate  bool       `json:"candidate,omitempty"`
	// Note explains, for candidates, why the evidence is insufficient
	// for an automatic rename/move conclusion.
	Note string `json:"note,omitempty"`
}

// Report is the full, deterministically ordered diff result.
type Report struct {
	Entries []Entry          `json:"entries"`
	Summary map[Category]int `json:"summary"`
}

// Options tunes matching thresholds.
type Options struct {
	MatchThreshold     float64
	CandidateThreshold float64
}

// DefaultOptions returns the default thresholds.
func DefaultOptions() Options {
	return Options{MatchThreshold: 0.85, CandidateThreshold: 0.5}
}

// DiffTrees compares two tree models and classifies every declaration.
func DiffTrees(oldTree, newTree *model.TreeModel, opts Options) *Report {
	if opts.MatchThreshold == 0 {
		opts = DefaultOptions()
	}
	d := &differ{opts: opts}
	d.index(oldTree, newTree)
	d.matchDirect()
	d.matchSimilar()
	d.finish()
	return d.report
}

// pair is a potential rename/move match between a removed and an
// added declaration.
type pair struct {
	old   model.Declaration
	new   model.Declaration
	score float64
	evid  []Evidence
}

type differ struct {
	opts     Options
	report   *Report
	oldByKey map[string]model.Declaration
	newByKey map[string]model.Declaration
	removed  []model.Declaration
	added    []model.Declaration
}

// index flattens both trees into key-indexed maps plus the pools of
// declarations that have no direct counterpart.
func (d *differ) index(oldTree, newTree *model.TreeModel) {
	d.report = &Report{Summary: map[Category]int{}}
	d.oldByKey = map[string]model.Declaration{}
	d.newByKey = map[string]model.Declaration{}
	collect := func(t *model.TreeModel, into map[string]model.Declaration) {
		if t == nil {
			return
		}
		paths := make([]string, 0, len(t.Files))
		for p := range t.Files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			for _, decl := range t.Files[p].Decls {
				into[decl.Key()] = decl
			}
		}
	}
	collect(oldTree, d.oldByKey)
	collect(newTree, d.newByKey)
}

// matchDirect classifies declarations whose key exists in both trees.
func (d *differ) matchDirect() {
	keys := make([]string, 0, len(d.oldByKey))
	for k := range d.oldByKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		oldDecl := d.oldByKey[k]
		newDecl, ok := d.newByKey[k]
		if !ok {
			d.removed = append(d.removed, oldDecl)
			continue
		}
		if oldDecl.ExactHash == newDecl.ExactHash {
			d.add(Entry{Category: Unchanged, Kind: newDecl.Kind, Name: newDecl.Name, File: newDecl.File})
		} else {
			d.add(Entry{Category: Modified, Kind: newDecl.Kind, Name: newDecl.Name, File: newDecl.File})
		}
	}
	newKeys := make([]string, 0, len(d.newByKey))
	for k := range d.newByKey {
		newKeys = append(newKeys, k)
	}
	sort.Strings(newKeys)
	for _, k := range newKeys {
		if _, ok := d.oldByKey[k]; !ok {
			d.added = append(d.added, d.newByKey[k])
		}
	}
}

// matchSimilar greedily pairs removed with added declarations by
// structural similarity, highest score first.
func (d *differ) matchSimilar() {
	var pairs []pair
	for _, r := range d.removed {
		for _, a := range d.added {
			if r.Kind != a.Kind {
				continue
			}
			score, evid := similarity(r, a)
			if score >= d.opts.CandidateThreshold {
				pairs = append(pairs, pair{old: r, new: a, score: score, evid: evid})
			}
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].score != pairs[j].score {
			return pairs[i].score > pairs[j].score
		}
		if pairs[i].old.Key() != pairs[j].old.Key() {
			return pairs[i].old.Key() < pairs[j].old.Key()
		}
		return pairs[i].new.Key() < pairs[j].new.Key()
	})
	usedOld := map[string]bool{}
	usedNew := map[string]bool{}
	for _, p := range pairs {
		if usedOld[p.old.Key()] || usedNew[p.new.Key()] {
			continue
		}
		usedOld[p.old.Key()] = true
		usedNew[p.new.Key()] = true
		d.addMatch(p)
	}
	var leftoverRemoved, leftoverAdded []model.Declaration
	for _, r := range d.removed {
		if !usedOld[r.Key()] {
			leftoverRemoved = append(leftoverRemoved, r)
		}
	}
	for _, a := range d.added {
		if !usedNew[a.Key()] {
			leftoverAdded = append(leftoverAdded, a)
		}
	}
	d.removed = leftoverRemoved
	d.added = leftoverAdded
}

// addMatch emits a rename/move (or candidate) entry for a matched pair.
func (d *differ) addMatch(p pair) {
	candidate := p.score < d.opts.MatchThreshold
	e := Entry{
		Kind:       p.new.Kind,
		Name:       p.new.Name,
		File:       p.new.File,
		Confidence: round4(p.score),
		Evidence:   p.evid,
		Candidate:  candidate,
	}
	switch {
	case p.old.File != p.new.File:
		e.Category = Moved
		e.OldFile = p.old.File
		if p.old.Name != p.new.Name || p.old.Receiver != p.new.Receiver {
			e.OldName = symbolName(p.old)
			e.Evidence = append(e.Evidence, Evidence{Kind: "renamed-during-move", Detail: "name changed while moving files", Score: 0})
		}
	default:
		e.Category = Renamed
		e.OldName = symbolName(p.old)
	}
	if candidate {
		e.Note = fmt.Sprintf("insufficient structural evidence for automatic %s: confidence %.2f below threshold %.2f; manual review advised",
			e.Category, round4(p.score), d.opts.MatchThreshold)
	}
	d.add(e)
}

// finish emits entries for unmatched declarations and sorts the report.
func (d *differ) finish() {
	for _, r := range d.removed {
		d.add(Entry{Category: Deleted, Kind: r.Kind, Name: symbolName(r), File: r.File})
	}
	for _, a := range d.added {
		d.add(Entry{Category: Added, Kind: a.Kind, Name: symbolName(a), File: a.File})
	}
	sort.SliceStable(d.report.Entries, func(i, j int) bool {
		a, b := d.report.Entries[i], d.report.Entries[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		if a.OldFile != b.OldFile {
			return a.OldFile < b.OldFile
		}
		return a.OldName < b.OldName
	})
}

func (d *differ) add(e Entry) {
	d.report.Entries = append(d.report.Entries, e)
	d.report.Summary[e.Category]++
}

func symbolName(d model.Declaration) string {
	if d.Receiver != "" {
		return d.Receiver + "." + d.Name
	}
	return d.Name
}

// similarity scores how likely two declarations are the same symbol.
//
// The score combines several independent evidence kinds, then gates the
// result on coverage: the smaller of the two declarations (in AST node
// count) bounds how much evidence can exist at all, so thin symbols can
// never reach the match threshold. Shape equality alone is therefore
// not sufficient for a high-confidence conclusion — a tiny empty
// function matches every other tiny empty function's shape.
//
//	coverage  = min(1, minNodeCount / fullCoverageNodes)
//	cap       = 0.5 + 0.5*coverage        // hard ceiling for the pair
//	weighted  = 0.60*hist + 0.28*ident + 0.08*name + 0.04*recv, capped at cap
//	shape     = cap * (0.75 + 0.25*ident) // only when shape hashes are equal
//	score     = max(weighted, shape)
func similarity(oldDecl, newDecl model.Declaration) (float64, []Evidence) {
	hist, histInter, histUnion := histSimilarity(oldDecl.TokenHist, newDecl.TokenHist)
	ident, identInter, identUnion := histSimilarity(oldDecl.IdentSet, newDecl.IdentSet)
	sizeOld, sizeNew := nodeCount(oldDecl.TokenHist), nodeCount(newDecl.TokenHist)
	minSize := sizeOld
	if sizeNew < minSize {
		minSize = sizeNew
	}
	coverage := float64(minSize) / fullCoverageNodes
	if coverage > 1 {
		coverage = 1
	}
	capScore := 0.5 + 0.5*coverage

	nameEq := 0.0
	if oldDecl.Name == newDecl.Name {
		nameEq = 1
	}
	recvEq := 0.0
	if oldDecl.Receiver != "" && oldDecl.Receiver == newDecl.Receiver {
		recvEq = 1
	}

	evid := []Evidence{
		{Kind: "structure", Detail: fmt.Sprintf("AST node-type histograms share %d of %d nodes", histInter, histUnion), Score: round4(hist)},
		{Kind: "identifiers", Detail: fmt.Sprintf("shared %d of %d identifier uses", identInter, identUnion), Score: round4(ident)},
		{Kind: "coverage", Detail: fmt.Sprintf("structural size %d and %d nodes (coverage %.2f)", sizeOld, sizeNew, coverage), Score: round4(coverage)},
	}
	if nameEq == 1 {
		evid = append(evid, Evidence{Kind: "name", Detail: "same symbol name", Score: 1})
	}
	if recvEq == 1 {
		evid = append(evid, Evidence{Kind: "receiver", Detail: "same receiver type " + oldDecl.Receiver, Score: 1})
	}

	weighted := 0.60*hist + 0.28*ident + 0.08*nameEq + 0.04*recvEq
	if weighted > capScore {
		weighted = capScore
	}
	score := weighted
	if oldDecl.ShapeHash == newDecl.ShapeHash {
		shapeScore := capScore * (0.75 + 0.25*ident)
		evid = append(evid, Evidence{
			Kind:   "shape",
			Detail: fmt.Sprintf("identical structure modulo declared name over %d nodes", minSize),
			Score:  round4(shapeScore),
		})
		if shapeScore > score {
			score = shapeScore
		}
	}
	return score, evid
}

// fullCoverageNodes is the declaration size (AST node count) at which
// structural evidence is considered fully covered.
const fullCoverageNodes = 16

// nodeCount sums a token histogram into a total AST node count.
func nodeCount(hist map[string]int) int {
	total := 0
	for _, n := range hist {
		total += n
	}
	return total
}

// histSimilarity is a weighted Jaccard index over count maps; it also
// reports the intersection and union sizes for verifiable evidence.
func histSimilarity(a, b map[string]int) (sim float64, inter, union int) {
	for k, av := range a {
		bv := b[k]
		if av < bv {
			inter += av
			union += bv
		} else {
			inter += bv
			union += av
		}
	}
	for k, bv := range b {
		if _, ok := a[k]; !ok {
			union += bv
		}
	}
	if union == 0 {
		return 1, 0, 0
	}
	return float64(inter) / float64(union), inter, union
}

func round4(f float64) float64 {
	return math.Round(f*10000) / 10000
}
