package diff

import (
	"strings"

	"github.com/guoyyit/source-structural-diff-go/internal/decl"
)

// Evidence weights. A rename or move with an untouched body reaches
// 0.55+0.30+0.15 = 1.0; with a lightly edited body it still clears
// the default accept threshold of 0.75.
const (
	wBody      = 0.55
	wSignature = 0.30
	wName      = 0.15
)

// tinyDeclTokens bounds the "tiny declaration" class. Tiny
// declarations (empty functions, one-line getters) share canonical
// bodies by coincidence, so body evidence is damped for them.
const tinyDeclTokens = 10

// tinyBodyDamping scales body evidence for tiny declarations.
const tinyBodyDamping = 0.3

// similarity scores how likely oldD and newD are the same declaration
// after a rename or move, returning a confidence in [0,1] plus the
// supporting evidence.
func similarity(oldD, newD *decl.Decl) (float64, []Evidence) {
	if oldD.Kind != newD.Kind {
		return 0, nil
	}
	var ev []Evidence
	conf := 0.0

	bodyWeight := wBody
	if min(oldD.TokenCount, newD.TokenCount) < tinyDeclTokens {
		bodyWeight *= tinyBodyDamping
	}

	if oldD.BodyHash == newD.BodyHash {
		conf += bodyWeight
		ev = append(ev, Evidence{
			Kind:   "identical-body",
			Detail: "canonical body is identical",
			Weight: bodyWeight,
		})
	} else if sim := bodySimilarity(oldD.Canon, newD.Canon); sim > 0.3 {
		w := bodyWeight * sim
		conf += w
		ev = append(ev, Evidence{
			Kind:   "body-similarity",
			Detail: "canonical bodies share structural tokens",
			Weight: w,
		})
	}

	if oldD.Signature == newD.Signature {
		conf += wSignature
		ev = append(ev, Evidence{
			Kind:   "identical-signature",
			Detail: "declaration signature is identical",
			Weight: wSignature,
		})
	}

	if oldD.Name == newD.Name {
		conf += wName
		ev = append(ev, Evidence{
			Kind:   "identical-name",
			Detail: "declared name is identical",
			Weight: wName,
		})
	} else if sim := nameSimilarity(oldD.Name, newD.Name); sim > 0.5 {
		w := wName * sim
		conf += w
		ev = append(ev, Evidence{
			Kind:   "name-similarity",
			Detail: "declared names are similar",
			Weight: w,
		})
	}

	return conf, ev
}

// bodySimilarity computes the Jaccard similarity of 3-gram token
// shingles over the canonical token streams.
func bodySimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	sa := shingles(tokenize(a), 3)
	sb := shingles(tokenize(b), 3)
	if len(sa) == 0 || len(sb) == 0 {
		return 0
	}
	inter := 0
	for s := range sa {
		if _, ok := sb[s]; ok {
			inter++
		}
	}
	union := len(sa) + len(sb) - inter
	return float64(inter) / float64(union)
}

// tokenize splits a canonical stream into atoms.
func tokenize(canon string) []string {
	return strings.FieldsFunc(canon, func(r rune) bool {
		return r == '(' || r == ')' || r == ' '
	})
}

// shingles builds the set of n-gram hashes over tokens.
func shingles(tokens []string, n int) map[uint64]struct{} {
	out := make(map[uint64]struct{}, len(tokens))
	if len(tokens) < n {
		// Tiny bodies: one shingle covering everything.
		out[hashTokens(tokens)] = struct{}{}
		return out
	}
	for i := 0; i+n <= len(tokens); i++ {
		out[hashTokens(tokens[i:i+n])] = struct{}{}
	}
	return out
}

func hashTokens(tokens []string) uint64 {
	h := uint64(1469598103934665603) // FNV-1a
	for _, t := range tokens {
		for i := 0; i < len(t); i++ {
			h ^= uint64(t[i])
			h *= 1099511628211
		}
		h ^= 0xff
		h *= 1099511628211
	}
	return h
}

// nameSimilarity is 1 - normalizedLevenshtein, computed on
// case-folded names.
func nameSimilarity(a, b string) float64 {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if a == b {
		return 1
	}
	d := levenshtein(a, b)
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}
	if maxLen == 0 {
		return 0
	}
	return 1 - float64(d)/float64(maxLen)
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 0
			if ra[i-1] != rb[j-1] {
				cost = 1
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
