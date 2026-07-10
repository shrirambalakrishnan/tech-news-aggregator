package rag

import (
	"fmt"
	"math"
	"sort"

	"github.com/shrirambalakrishnan/tech-news/voyageapi"
)

// This file is the RETRIEVAL stage: given a query, find the corpus chunks most
// relevant to it. It consumes the index artifact (index.go) that the embed step
// wrote; nothing here touches the corpus sources or the chunking logic.

// RETRIEVAL_TOP_K is how many chunks retrieval returns for injection into the
// classifier prompt. The main quality/cost knob: each chunk is ~800 words, so k
// scales the classifier's input tokens roughly linearly.
var RETRIEVAL_TOP_K = 5

// DI seams (function-variable convention): tests swap these to run retrieval
// without disk or network.
var (
	embedQuery      = voyageapi.EmbedQuery
	loadCorpusIndex = LoadCorpusIndex
)

// cosineSimilarity measures how aligned two vectors are, ignoring magnitude:
// dot(a,b) / (|a|·|b|), in [-1, 1] where 1 means "pointing the same way".
// Mismatched-length or zero vectors score 0 — nothing can match them, and
// returning 0 keeps a single malformed chunk from breaking the whole ranking.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

type scoredChunk struct {
	chunk Chunk
	score float64
}

// topKBySimilarity returns the k chunks most similar to queryVec, best first.
// A full sort is O(n log n) but the index holds a few hundred chunks — not
// worth a heap. SliceStable keeps equal-scored chunks in index order so
// results are deterministic.
func topKBySimilarity(chunks []Chunk, queryVec []float32, k int) []Chunk {
	scored := make([]scoredChunk, 0, len(chunks))
	for _, c := range chunks {
		scored = append(scored, scoredChunk{chunk: c, score: cosineSimilarity(queryVec, c.Embedding)})
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })

	k = min(k, len(scored))
	if k < 0 {
		k = 0
	}
	top := make([]Chunk, 0, k)
	for _, s := range scored[:k] {
		top = append(top, s.chunk)
	}
	return top
}

// RetrieveContext returns the texts of the k corpus chunks most similar to
// query, best match first. Callers must fail soft on error — the index may not
// exist (the embed step hasn't run), and classification should fall back to
// the profile/static rules rather than abort, mirroring LoadUserContext.
func RetrieveContext(query string, k int) ([]string, error) {
	// Load the index before embedding: a missing index is the common failure
	// mode and is free to detect, while the embedding costs a network call.
	index, err := loadCorpusIndex(CORPUS_INDEX_FILE)
	if err != nil {
		return nil, fmt.Errorf("corpus index unavailable: %w", err)
	}

	queryVec, err := embedQuery(query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed retrieval query: %w", err)
	}

	texts := []string{}
	for _, chunk := range topKBySimilarity(index.Chunks, queryVec, k) {
		texts = append(texts, chunk.Text)
	}
	return texts, nil
}
