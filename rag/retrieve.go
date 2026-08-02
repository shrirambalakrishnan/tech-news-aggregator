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

// Knobs for arm 3's per-story retrieval (RetrievePooledContext). Arm 2 retrieves
// once per batch with one blended query; arm 3 retrieves per story and pools the
// results, so it needs its own sizing.
var (
	// RETRIEVAL_TOP_K_PER_STORY is k for a single story's query. Smaller than
	// RETRIEVAL_TOP_K because a per-story query is specific — 5 was sized for
	// one query standing in for ~30 stories.
	RETRIEVAL_TOP_K_PER_STORY = 2

	// RETRIEVAL_SIMILARITY_FLOOR drops chunks scoring below it, so a story with
	// no real corpus support contributes nothing instead of contributing the
	// least-irrelevant chunks. 0 keeps everything that is not anti-correlated,
	// i.e. effectively no filtering: the correct value is a property of this
	// corpus and must be MEASURED with `go run . calibrate` (step 3 of the
	// plan on issue #19) before arm 3 is scored. Guessing it would make
	// "the floor didn't help" unfalsifiable — voyage-4-lite's dynamic range is
	// unknown, and against a distribution clustered in 0.7–0.9 a guessed 0.4
	// filters nothing.
	RETRIEVAL_SIMILARITY_FLOOR = 0.0

	// RETRIEVAL_POOL_CAP is the hard ceiling on pooled chunks per call. It is
	// what keeps arm 3 retrieval rather than long-context stuffing: it bounds
	// the prompt cost independently of the floor, since truncation happens
	// after pooling.
	RETRIEVAL_POOL_CAP = 20
)

// DI seams (function-variable convention): tests swap these to run retrieval
// without disk or network.
var (
	embedQuery      = voyageapi.EmbedQuery
	embedQueries    = voyageapi.EmbedQueries
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

// topKAboveFloor is topKBySimilarity with a relevance floor: chunks scoring
// below floor are dropped *before* truncating to k, so a query with no real
// corpus support returns an empty slice rather than k weak matches. That empty
// return is the point of the floor, not an edge case.
//
// It returns scoredChunk rather than Chunk because the caller (poolChunks)
// needs the scores to merge results across queries.
func topKAboveFloor(chunks []Chunk, queryVec []float32, k int, floor float64) []scoredChunk {
	scored := make([]scoredChunk, 0, len(chunks))
	for _, c := range chunks {
		score := cosineSimilarity(queryVec, c.Embedding)
		if score < floor {
			continue
		}
		scored = append(scored, scoredChunk{chunk: c, score: score})
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })

	k = min(k, len(scored))
	if k < 0 {
		k = 0
	}
	return scored[:k]
}

// chunkKey identifies a chunk for deduplication. Identity is its provenance —
// which file and which window — not its text: two windows can overlap heavily
// (chunking uses a 120-word overlap) without being the same chunk, and text
// comparison would be both wrong and expensive.
type chunkKey struct {
	Source     string
	ChunkIndex int
}

// poolChunks merges per-story results into one ranked list: dedupe, keeping the
// highest score a chunk earned against any story, then order best-first and
// truncate to poolCap. A chunk retrieved by several stories is stronger
// evidence, not duplicated context — so it appears once, at its best score.
//
// Ties keep first-seen order (which follows the caller's story order), so the
// same inputs always produce the same pool.
func poolChunks(perStory [][]scoredChunk, poolCap int) []Chunk {
	best := map[chunkKey]int{} // key -> position in pooled
	var pooled []scoredChunk

	for _, results := range perStory {
		for _, s := range results {
			key := chunkKey{Source: s.chunk.Source, ChunkIndex: s.chunk.ChunkIndex}
			if pos, seen := best[key]; seen {
				if s.score > pooled[pos].score {
					pooled[pos].score = s.score
				}
				continue
			}
			best[key] = len(pooled)
			pooled = append(pooled, s)
		}
	}

	sort.SliceStable(pooled, func(i, j int) bool { return pooled[i].score > pooled[j].score })

	n := min(poolCap, len(pooled))
	if n < 0 {
		n = 0
	}
	chunks := make([]Chunk, 0, n)
	for _, s := range pooled[:n] {
		chunks = append(chunks, s.chunk)
	}
	return chunks
}

// RetrievePooledContext is arm 3's retrieval: one query per story, each filtered
// by RETRIEVAL_SIMILARITY_FLOOR and cut to RETRIEVAL_TOP_K_PER_STORY, pooled
// into a single deduplicated list of at most RETRIEVAL_POOL_CAP chunk texts,
// best match first.
//
// It differs from RetrieveContext (arm 2) in exactly one variable — how
// excerpts are selected — so that a delta in the eval is attributable to
// per-story retrieval and nothing else. All the queries travel as one Voyage
// request (voyageapi.EmbedQueries batches them), so this costs the same single
// round trip arm 2 pays.
//
// Errors propagate; callers must NOT fail soft, for the same reason as
// RetrieveContext.
func RetrievePooledContext(queries []string) ([]string, error) {
	// Load the index before embedding: a missing index is the common failure
	// mode and is free to detect, while the embedding costs a network call.
	index, err := loadCorpusIndex(CORPUS_INDEX_FILE)
	if err != nil {
		return nil, fmt.Errorf("corpus index unavailable: %w", err)
	}

	queryVecs, err := embedQueries(queries)
	if err != nil {
		return nil, fmt.Errorf("failed to embed retrieval queries: %w", err)
	}
	if len(queryVecs) != len(queries) {
		return nil, fmt.Errorf("embedded %d queries, expected %d", len(queryVecs), len(queries))
	}

	perStory := make([][]scoredChunk, 0, len(queryVecs))
	for _, vec := range queryVecs {
		perStory = append(perStory, topKAboveFloor(index.Chunks, vec, RETRIEVAL_TOP_K_PER_STORY, RETRIEVAL_SIMILARITY_FLOOR))
	}

	texts := []string{}
	for _, chunk := range poolChunks(perStory, RETRIEVAL_POOL_CAP) {
		texts = append(texts, chunk.Text)
	}
	return texts, nil
}

// RetrieveContext returns the texts of the k corpus chunks most similar to
// query, best match first. An error usually means the index is missing (the
// embed step hasn't run). Callers must NOT fail soft on it: under explicit arm
// selection a run asked for arm 2 has to exit non-zero rather than quietly
// classify under arm 1's profile or arm 0's static rules, which would publish
// numbers labelled "RAG" that were really another arm's (issue #11).
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
