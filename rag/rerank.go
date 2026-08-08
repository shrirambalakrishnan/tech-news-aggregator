package rag

import (
	"fmt"
	"log"

	"github.com/shrirambalakrishnan/tech-news/voyageapi"
)

// This file is arm 4's RETRIEVAL stage: the same per-story retrieval arm 3 does,
// with a cross-encoder inserted between selecting candidates and pooling them.
//
// Why a second stage at all. Cosine compares two vectors built independently
// that never saw each other: a chunk's vector was computed at embed time, before
// any story existed, and compresses ~800 words into one point — a topical
// average, so a chunk whose relevance lives in one paragraph out of eight is
// averaged down by the other seven. A cross-encoder reads the title and the
// chunk together in one forward pass, so nothing is averaged and nothing is
// precomputed. That is also why it cannot replace cosine: it emits no reusable
// vector, so it must run live per pair, and running it over all 168 chunks per
// story would cost 168 pairs instead of 6. Cosine narrows, the cross-encoder
// reorders.
//
// ⚠️ THE RERANK SCORE REORDERS; IT DOES NOT FILTER. RERANK_TOP_K_PER_STORY
// chunks are kept per story unconditionally, and RETRIEVAL_POOL_CAP truncates
// the pool afterwards. Nothing is dropped for scoring low. Issue #27 expected
// the cross-encoder to do the filtering that RETRIEVAL_SIMILARITY_FLOOR does for
// arm 3; shipping reorder-only keeps arm 4 a single-variable change against arm
// 3, and follows this repo's own calibrate-floor lesson — measure the scores
// before building a knob on them. The per-(story, chunk) scores are logged
// below, so a rerank floor can be calibrated from the arm-4 eval run itself
// rather than by paying for a second one.

// Knobs for arm 4's rerank stage.
var (
	// RERANK_CANDIDATES_PER_STORY is a COUNT of chunks per story handed to the
	// cross-encoder, >= RERANK_TOP_K_PER_STORY. Bounded by the 10K tokens/min
	// account budget: 6 chunks of ~800 words is ~7.9K tokens, 79% of the
	// minute, which is where the ~52s pacing per story comes from. Raising it
	// raises the pacing delay — and so the run's wall clock — roughly linearly.
	// 6 chunks + 1 title is also the shape the /v1/rerank rate-limit probe was
	// run at, so this value is where the pacing is known to hold.
	RERANK_CANDIDATES_PER_STORY = 6

	// RERANK_TOP_K_PER_STORY is a COUNT of chunks kept per story after
	// reranking, <= RERANK_CANDIDATES_PER_STORY. Deliberately equal to arm 3's
	// RETRIEVAL_TOP_K_PER_STORY so the pooled excerpt volume stays comparable
	// between the arms and any delta is about which chunks were chosen, not how
	// many.
	RERANK_TOP_K_PER_STORY = 2

	// RERANK_CANDIDATE_FLOOR is a cosine similarity in [-1, 1], applied at the
	// CANDIDATE stage.
	//
	// ⚠️ INERT AT THIS VALUE — it is a placeholder for a filter this arm does
	// not yet have, not a tuned setting. Candidates are selected by RANK
	// (RERANK_CANDIDATES_PER_STORY = 6), not by score, so any floor below the
	// 6th-ranked chunk's cosine changes nothing. RETRIEVAL_SIMILARITY_FLOOR
	// shipped at 0.0 and inert once already, and what that cost is recorded in
	// CLAUDE.md: a knob that looks tuned but cannot move invites someone to tune
	// it and wonder why nothing changed.
	//
	// It is a SEPARATE var from RETRIEVAL_SIMILARITY_FLOOR rather than a reuse,
	// because the two are opposite in intent. That one is arm 3's relevance
	// filter — the last decision about what reaches the prompt, so it wants to
	// be strict. This one is a recall filter feeding the cross-encoder, so it
	// wants to be permissive enough that nothing the reranker could rescue is
	// discarded before it gets a look. One constant cannot serve both. Arm 3's
	// value stays at 0.3330, untouched, so arm 3's recorded numbers stand.
	RERANK_CANDIDATE_FLOOR = 0.0
)

// rerankQueries is the DI seam for the cross-encoder call (the repo-wide
// function-variable convention), so retrieval can be tested without the network.
var rerankQueries = voyageapi.RerankMany

// candidateSet is one story's shortlist: the query that produced it and the
// chunks cosine selected, in cosine order. It is kept as a pair because the
// rerank response addresses documents by their position in the SUBMITTED list —
// losing the pairing would silently attach every score to the wrong chunk.
type candidateSet struct {
	query      string
	candidates []scoredChunk
}

// RetrieveRerankedContext is arm 4's retrieval: one query per story, narrowed to
// RERANK_CANDIDATES_PER_STORY chunks by cosine, rescored by the cross-encoder,
// cut to RERANK_TOP_K_PER_STORY per story, then pooled into a single
// deduplicated list of at most RETRIEVAL_POOL_CAP chunk texts, best first.
//
// It differs from RetrievePooledContext (arm 3) in exactly one place — how the
// per-story chunks are ranked. Same corpus, same index, same queries (the
// batch's titles), same pooling, same cap, same prompt downstream.
//
// Two caveats that come with the cross-encoder score:
//
//   - The pool is now ordered and deduped by RELEVANCE SCORE, which is produced
//     per (query, document) pair and is only loosely comparable across queries.
//     Cosine already carried that caveat; this is a sharper version of it, since
//     nothing normalizes a cross-encoder's outputs between queries.
//   - The cosine stage is now purely a candidate RECALL filter. It no longer
//     decides what reaches the prompt, so RETRIEVAL_SIMILARITY_FLOOR is not
//     consulted here at all (see RERANK_CANDIDATE_FLOOR).
//
// Errors propagate; callers must NOT fail soft, for the same reason as
// RetrieveContext — an arm asked for must run as that arm or not at all.
func RetrieveRerankedContext(queries []string) ([]string, error) {
	// Load the index before embedding: a missing index is the common failure
	// mode and is free to detect, while embedding costs a network call.
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

	sets := selectCandidates(index.Chunks, queries, queryVecs)
	if len(sets) == 0 {
		// Every story's shortlist was empty, which at RERANK_CANDIDATE_FLOOR =
		// 0.0 means the index itself is empty. Returning no excerpts is the
		// honest answer; sending an empty rerank request would not be.
		log.Printf("rag: arm 4 found no candidates for any of %d stories — is the corpus index empty?", len(queries))
		return []string{}, nil
	}

	perStory, err := rerankCandidateSets(sets)
	if err != nil {
		return nil, err
	}

	pooled := poolChunks(perStory, RETRIEVAL_POOL_CAP)

	// The pooled COUNT is logged because it is the number that decides whether
	// an arm-4 vs arm-3 comparison is volume-matched, and README records twice
	// that assuming this number instead of counting it produced a wrong
	// conclusion. It was previously recovered by grepping "--- Excerpt N ---"
	// out of old run logs after the fact.
	log.Printf("rag: arm 4 pooled %d excerpts (cap %d) from %d of %d stories with candidates",
		len(pooled), RETRIEVAL_POOL_CAP, len(sets), len(queries))

	texts := []string{}
	for _, chunk := range pooled {
		texts = append(texts, chunk.Text)
	}
	return texts, nil
}

// selectCandidates runs the cosine stage: each story's shortlist, in query
// order, with empty shortlists dropped.
//
// A story with no candidates is SKIPPED rather than carried as an empty entry —
// an empty document list is a request the reranker cannot answer, and paying a
// ~52s pacing delay to send one would be worse than pointless.
func selectCandidates(chunks []Chunk, queries []string, queryVecs [][]float32) []candidateSet {
	sets := make([]candidateSet, 0, len(queries))
	for i, vec := range queryVecs {
		candidates := topKAboveFloor(chunks, vec, RERANK_CANDIDATES_PER_STORY, RERANK_CANDIDATE_FLOOR)
		if len(candidates) == 0 {
			continue
		}
		sets = append(sets, candidateSet{query: queries[i], candidates: candidates})
	}
	return sets
}

// rerankCandidateSets scores every shortlist with the cross-encoder and returns
// each story's kept chunks carrying their RELEVANCE score in place of their
// cosine one — which is what makes poolChunks rank and dedupe on the
// cross-encoder's opinion instead of the embeddings'.
func rerankCandidateSets(sets []candidateSet) ([][]scoredChunk, error) {
	queries := make([]string, 0, len(sets))
	documents := make([][]string, 0, len(sets))
	for _, set := range sets {
		queries = append(queries, set.query)
		texts := make([]string, 0, len(set.candidates))
		for _, c := range set.candidates {
			texts = append(texts, c.chunk.Text)
		}
		documents = append(documents, texts)
	}

	results, err := rerankQueries(queries, documents, RERANK_TOP_K_PER_STORY)
	if err != nil {
		return nil, fmt.Errorf("failed to rerank retrieval candidates: %w", err)
	}
	if len(results) != len(sets) {
		return nil, fmt.Errorf("reranked %d stories, expected %d", len(results), len(sets))
	}

	perStory := make([][]scoredChunk, 0, len(sets))
	for i, set := range sets {
		kept := make([]scoredChunk, 0, len(results[i]))
		for _, result := range results[i] {
			// The index addresses this story's submitted candidates. Checked
			// here as well as in voyageapi because the seam above is swappable
			// and this is where an out-of-range value would panic.
			if result.Index < 0 || result.Index >= len(set.candidates) {
				return nil, fmt.Errorf("rerank result index %d out of range for %d candidates of query %q", result.Index, len(set.candidates), set.query)
			}
			candidate := set.candidates[result.Index]
			kept = append(kept, scoredChunk{chunk: candidate.chunk, score: result.RelevanceScore})
			logRerankScore(set.query, candidate, result.RelevanceScore)
		}
		perStory = append(perStory, kept)
	}
	return perStory, nil
}

// logRerankScore emits one CSV-shaped line per kept (story, chunk) pair, so the
// run log doubles as the score dump a rerank floor would be calibrated from.
//
// Logged rather than written to a file on purpose: rag writes the corpus index
// and nothing else, and eval-run-logs/ is already where this repo recovers
// per-run numbers from. The query is quoted because titles contain commas.
func logRerankScore(query string, candidate scoredChunk, rerankScore float64) {
	log.Printf("rag: rerank-score,%q,%s,%d,%.6f,%.6f",
		query, candidate.chunk.Source, candidate.chunk.ChunkIndex, candidate.score, rerankScore)
}
