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
	// Note explains, in a distinguishable way, why an entry landed in
	// candidate matching instead of a confident classification.
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
	// MinEvidenceNodes is the minimum structural-evidence mass (count of
	// AST nodes, identifiers and literals of the smaller side) required
	// for a high-confidence rename/move verdict. Smaller symbols can
	// still match, but their confidence is capped below MatchThreshold
	// and they are reported as candidates. Zero selects the default.
	MinEvidenceNodes int
}

// DefaultOptions returns the default thresholds.
func DefaultOptions() Options {
	return Options{MatchThreshold: 0.85, CandidateThreshold: 0.5, MinEvidenceNodes: 20}
}

// DiffTrees compares two tree models and classifies every declaration.
func DiffTrees(oldTree, newTree *model.TreeModel, opts Options) *Report {
	if opts.MatchThreshold == 0 {
		opts = DefaultOptions()
	}
	if opts.MinEvidenceNodes <= 0 {
		opts.MinEvidenceNodes = DefaultOptions().MinEvidenceNodes
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
	nodes int
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
			score, evid, nodes := similarity(r, a)
			if score >= d.opts.CandidateThreshold {
				pairs = append(pairs, pair{old: r, new: a, score: score, evid: evid, nodes: nodes})
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
// Confidence is capped below the match threshold when the evidence mass
// is too small to support a high-confidence verdict, unless the content
// is token-for-token identical (which is decisive at any size).
func (d *differ) addMatch(p pair) {
	score := p.score
	var note string
	exact := p.old.ExactHash == p.new.ExactHash
	if !exact && p.nodes < d.opts.MinEvidenceNodes && score >= d.opts.MatchThreshold {
		score = d.opts.MatchThreshold - 0.01
		note = fmt.Sprintf("insufficient structural evidence: only %d evidence nodes (need %d); confidence capped below match threshold",
			p.nodes, d.opts.MinEvidenceNodes)
	}
	candidate := score < d.opts.MatchThreshold
	if candidate && note == "" {
		note = fmt.Sprintf("best-effort match: score %.4f below match threshold %.2f", score, d.opts.MatchThreshold)
	}
	e := Entry{
		Kind:       p.new.Kind,
		Name:       p.new.Name,
		File:       p.new.File,
		Confidence: round4(score),
		Evidence:   p.evid,
		Candidate:  candidate,
		Note:       note,
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
// Name equality alone is deliberately weak: structurally unrelated
// same-name symbols must never reach the match threshold. The returned
// nodes value is the evidence mass (profile size of the smaller side),
// used to cap confidence when there is too little structure to judge.
func similarity(oldDecl, newDecl model.Declaration) (score float64, evid []Evidence, nodes int) {
	nodes = min(profileSize(oldDecl), profileSize(newDecl))

	// Token-for-token identical content (modulo formatting/comments)
	// is decisive regardless of symbol size: it cannot be a
	// coincidence of shape.
	if oldDecl.ExactHash == newDecl.ExactHash {
		return 1.0, []Evidence{
			{Kind: "exact", Detail: "identical token stream modulo formatting", Score: 1},
		}, nodes
	}

	hist := histSimilarity(oldDecl.TokenHist, newDecl.TokenHist)
	ident := histSimilarity(oldDecl.IdentSet, newDecl.IdentSet)
	nameEq := 0.0
	if oldDecl.Name == newDecl.Name {
		nameEq = 1
	}
	recvEq := 0.0
	if oldDecl.Receiver == newDecl.Receiver {
		recvEq = 1
	}
	score = 0.55*hist + 0.25*ident + 0.10*nameEq + 0.05*recvEq
	evid = []Evidence{
		{Kind: "structure", Detail: "AST node-type histogram similarity", Score: round4(hist)},
		{Kind: "identifiers", Detail: "identifier usage similarity", Score: round4(ident)},
	}
	if oldDecl.ShapeHash == newDecl.ShapeHash {
		score = math.Max(score, 0.95)
		evid = append(evid, Evidence{Kind: "shape", Detail: "identical structure modulo declared name", Score: 1})
	}
	if nameEq == 1 {
		evid = append(evid, Evidence{Kind: "name", Detail: "same symbol name", Score: 1})
	}
	if recvEq == 1 && oldDecl.Receiver != "" {
		evid = append(evid, Evidence{Kind: "receiver", Detail: "same receiver type " + oldDecl.Receiver, Score: 1})
	}
	return score, evid, nodes
}

// profileSize is the structural-evidence mass of a declaration: the
// total count of AST nodes, identifiers and literals in its profile.
func profileSize(d model.Declaration) int {
	n := 0
	for _, c := range d.TokenHist {
		n += c
	}
	return n
}

// histSimilarity is a weighted Jaccard index over count maps.
func histSimilarity(a, b map[string]int) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	inter, union := 0, 0
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
		return 1
	}
	return float64(inter) / float64(union)
}

func round4(f float64) float64 {
	return math.Round(f*10000) / 10000
}
