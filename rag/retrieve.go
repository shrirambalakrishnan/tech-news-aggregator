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
	// corpus and must be MEASURED with `go run . calibrate-floor` (step 3 of
	// the plan on issue #19) before arm 3 is scored. Guessing it would make
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

// scoredChunks is a ranked list of chunks with the score each earned during
// retrieval. Pooling is a four-step pipeline over such a list, so each step is a
// method that takes one list and returns a new one — letting poolChunks read as
// the sequence of steps it is, and letting each step be understood (and tested)
// on its own.
type scoredChunks []scoredChunk

// poolChunks merges the per-story retrieval results into one ranked list of
// chunks to inject into the prompt. In order:
//
//  1. flatten                  — one list instead of one list per story
//  2. dedupeKeepingBestScore   — each chunk appears once, at its best score
//  3. sortedByScoreDesc        — best evidence first
//  4. truncatedTo(poolCap)     — bound the prompt cost
//
// Steps 2 and 3 are where the semantics live: a chunk retrieved by several
// stories is stronger evidence rather than duplicated context, so it survives
// once and is ranked by its strongest match.
func poolChunks(perStory [][]scoredChunk, poolCap int) []Chunk {
	return flatten(perStory).
		dedupeKeepingBestScore().
		sortedByScoreDesc().
		truncatedTo(poolCap).
		chunks()
}

// flatten concatenates the per-story result lists into a single list, keeping
// story order and, within a story, rank order. That ordering is what makes ties
// deterministic downstream: equal scores keep first-seen position.
func flatten(perStory [][]scoredChunk) scoredChunks {
	pooled := scoredChunks{}
	for _, results := range perStory {
		pooled = append(pooled, results...)
	}
	return pooled
}

// dedupeKeepingBestScore collapses repeats of the same chunk into one entry
// carrying the highest score it earned against any story. Position is the entry's
// first appearance; only the score is updated by later hits.
func (s scoredChunks) dedupeKeepingBestScore() scoredChunks {
	positionOf := map[chunkKey]int{}
	deduped := scoredChunks{}

	for _, sc := range s {
		key := chunkKey{Source: sc.chunk.Source, ChunkIndex: sc.chunk.ChunkIndex}
		position, alreadyPooled := positionOf[key]
		if !alreadyPooled {
			positionOf[key] = len(deduped)
			deduped = append(deduped, sc)
			continue
		}
		if sc.score > deduped[position].score {
			deduped[position].score = sc.score
		}
	}
	return deduped
}

// sortedByScoreDesc orders the list best-first, leaving the receiver untouched.
// The sort is stable, so equal scores keep the order flatten established.
func (s scoredChunks) sortedByScoreDesc() scoredChunks {
	sorted := append(scoredChunks{}, s...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].score > sorted[j].score })
	return sorted
}

// truncatedTo keeps at most n entries. A negative n is treated as 0.
func (s scoredChunks) truncatedTo(n int) scoredChunks {
	n = min(n, len(s))
	if n < 0 {
		n = 0
	}
	return s[:n]
}

// chunks drops the scores, which exist only to rank and dedupe — callers past
// this point just need the chunks themselves.
func (s scoredChunks) chunks() []Chunk {
	out := make([]Chunk, 0, len(s))
	for _, sc := range s {
		out = append(out, sc.chunk)
	}
	return out
}

// RetrievePooledContext is arm 3's retrieval: one query per story, each filtered
// by RETRIEVAL_SIMILARITY_FLOOR and cut to RETRIEVAL_TOP_K_PER_STORY, pooled
// into a single deduplicated list of at most RETRIEVAL_POOL_CAP chunk texts,
// best match first.
//
// It changes TWO things against RetrieveContext (arm 2), not one. Selection is
// the intended variable — per-story queries pooled, instead of one blended
// query. Excerpt VOLUME is the unintended one: arm 2 injects exactly
// RETRIEVAL_TOP_K (5) chunks, this returns anywhere up to RETRIEVAL_POOL_CAP
// (20). Excerpts are inlined verbatim into the prompt, so whenever the pool
// exceeds 5 the arms are also being compared at different prompt sizes, and a
// delta between them is confounded between "per-story retrieval didn't help"
// and "the bigger prompt hurt". How far the pool actually ran in the scored run
// is NOT recorded — the available figures are either derived from the very
// assumption they would be proving or dedupe-blind, so treat 20 as a ceiling,
// not a measurement. Setting RETRIEVAL_POOL_CAP = RETRIEVAL_TOP_K and re-running
// both arms is what makes the comparison genuinely single-variable; until that
// is done, see README -> "Eval - Execution results" for how the recorded numbers
// are qualified. All the queries travel as one Voyage request
// (voyageapi.EmbedQueries batches them), so this costs the same single round
// trip arm 2 pays.
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

// BestSimilarityPerQuery returns, for each query in order, the highest cosine
// similarity any corpus chunk scores against it. That single number is exactly
// what RETRIEVAL_SIMILARITY_FLOOR is compared against per story: a story whose
// best score falls below the floor contributes no excerpts at all. Calibrating
// the floor therefore means looking at the distribution of these numbers.
func BestSimilarityPerQuery(queries []string) ([]float64, error) {
	perQuery, err := TopScoresPerQuery(queries, 1)
	if err != nil {
		return nil, err
	}

	best := make([]float64, 0, len(perQuery))
	for _, scores := range perQuery {
		// A query scores nothing only if the index is empty, which is already
		// rejected above; -1 is the cosine floor, so it is the safe stand-in.
		if len(scores) == 0 {
			best = append(best, -1.0)
			continue
		}
		best = append(best, scores[0])
	}
	return best, nil
}

// TopScoresPerQuery returns, for each query in order, the scores of that query's
// k best-matching corpus chunks, highest first. It is the measurement behind the
// calibration step: k=1 gives the per-story number the floor is compared
// against, and k=RETRIEVAL_TOP_K_PER_STORY gives what each story would actually
// contribute to the pool, which is what the pool-cap simulation needs.
//
// Scores only — the chunk texts are deliberately not returned, so the caller
// cannot accidentally turn a measurement into a retrieval.
//
// It exists so calibration can measure without exporting cosineSimilarity or the
// index internals, and it costs one Voyage request per batch of queries and no
// Claude call at all.
func TopScoresPerQuery(queries []string, k int) ([][]float64, error) {
	perQuery, err := topChunksPerQuery(queries, k)
	if err != nil {
		return nil, err
	}

	scoresPerQuery := make([][]float64, 0, len(perQuery))
	for _, ranked := range perQuery {
		scores := make([]float64, 0, len(ranked))
		for _, sc := range ranked {
			scores = append(scores, sc.score)
		}
		scoresPerQuery = append(scoresPerQuery, scores)
	}
	return scoresPerQuery, nil
}

// TopSourcesPerQuery returns, for each query in order, the FILENAME of its
// best-matching corpus chunk (Chunk.Source) — "which document won this story?".
//
// It answers a question the scores alone cannot: whether a corpus change
// actually displaced the documents that were previously winning. Before reading
// notes were added, one README was the top match for 86 of the 341 labelled
// titles; whether that share drops is the most direct evidence for the
// displacement a corpus addition is betting on, and it costs no Claude call.
//
// Sources only, never chunk text — same boundary as TopScoresPerQuery: a caller
// holding filenames cannot accidentally turn a measurement into a retrieval.
// A query whose ranking is empty yields "" (only possible on an empty index,
// which is rejected before embedding).
//
// Cost: one Voyage request per batch of queries (voyageapi.EmbedQueries),
// $0.00 on the free tier. Calling it alongside TopScoresPerQuery re-embeds the
// same queries, so calibration pays two paced round trips instead of one — free
// but not instant at 3 RPM.
func TopSourcesPerQuery(queries []string) ([]string, error) {
	perQuery, err := topChunksPerQuery(queries, 1)
	if err != nil {
		return nil, err
	}

	sources := make([]string, 0, len(perQuery))
	for _, ranked := range perQuery {
		if len(ranked) == 0 {
			sources = append(sources, "")
			continue
		}
		sources = append(sources, ranked[0].chunk.Source)
	}
	return sources, nil
}

// topChunksPerQuery is the shared body of the measurement functions above: load
// the index, embed every query in one request, and rank the whole index against
// each. It stays unexported so the chunk texts it carries never leave the
// package — the exported wrappers project out one field each.
func topChunksPerQuery(queries []string, k int) ([]scoredChunks, error) {
	index, err := loadCorpusIndex(CORPUS_INDEX_FILE)
	if err != nil {
		return nil, fmt.Errorf("corpus index unavailable: %w", err)
	}
	if len(index.Chunks) == 0 {
		return nil, fmt.Errorf("corpus index holds no chunks: run `go run . embed` first")
	}

	queryVecs, err := embedQueries(queries)
	if err != nil {
		return nil, fmt.Errorf("failed to embed calibration queries: %w", err)
	}
	if len(queryVecs) != len(queries) {
		return nil, fmt.Errorf("embedded %d queries, expected %d", len(queryVecs), len(queries))
	}

	perQuery := make([]scoredChunks, 0, len(queryVecs))
	for _, vec := range queryVecs {
		ranked := make(scoredChunks, 0, len(index.Chunks))
		for _, chunk := range index.Chunks {
			ranked = append(ranked, scoredChunk{chunk: chunk, score: cosineSimilarity(vec, chunk.Embedding)})
		}
		perQuery = append(perQuery, ranked.sortedByScoreDesc().truncatedTo(k))
	}
	return perQuery, nil
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
