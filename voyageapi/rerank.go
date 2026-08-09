package voyageapi

import (
	"errors"
	"fmt"
	"log"
	"time"
)

// This file is the PUBLIC API of the rerank endpoint, the second half of a
// two-stage retrieval: embeddings narrow the corpus by cosine similarity, then a
// cross-encoder rescores the survivors.
//
// What makes a second network call worth it: an embedding is computed once, at
// embed time, before any query exists, so a chunk's vector is a topical average
// of its ~800 words. A cross-encoder reads the query and the document together
// in one forward pass, so relevance living in one paragraph is not averaged away
// by the other seven. The cost is that nothing is precomputable — a
// cross-encoder emits no reusable vector, so it must run live for every (query,
// document) pair, which is why it runs over a handful of candidates and never
// the whole index.
//
// Rate limiting is NOT duplicated here: the 3 RPM / 10K TPM throttle is a
// property of the no-payment ACCOUNT, not of the embeddings endpoint, so this
// path reuses ratelimit.go's constants (VOYAGE_TPM_LIMIT,
// VOYAGE_MIN_REQUEST_GAP, VOYAGE_MAX_RETRIES, VOYAGE_RETRY_BACKOFF) and its
// pacingDelay formula. One place to change when the account tier does.

// RERANK_TOKENS_PER_CALL is the estimated token cost of ONE rerank request at
// the shape arm 4 sends: rag.RERANK_CANDIDATES_PER_STORY (6) chunks of ~800
// words plus a ~9-word story title. Measured, not computed per call — the shape
// is fixed, so there is nothing to vary.
//
// It is 79% of the 10K/min budget, which is where the ~52s pacing between calls
// comes from. ⚠️ RAISE THIS IF rag.RERANK_CANDIDATES_PER_STORY RISES:
// under-estimating here does not save time, it just turns the pacing into 429s
// and 1min/2min/3min backoffs, which is slower than waiting.
var RERANK_TOKENS_PER_CALL = 7859

// RerankResult is one scored document. Index is its position in the SUBMITTED
// documents slice, which is what maps a score back to the chunk it came from:
// the API returns results reordered by score, so position in the response says
// nothing about which document was scored.
type RerankResult struct {
	Index          int
	RelevanceScore float64
}

// rerankBatch is the network seam (function-variable DI): one query and its
// documents in one HTTP call. Routed through a package var so tests can exercise
// pacing, retry and result mapping without hitting the network.
var rerankBatch = rerankHTTP

// Rerank scores documents against a single query, returning ONE result per
// submitted document, best first. One query, one request; pacing between
// requests is RerankMany's job, so a lone call pays no delay — only the 429
// retry backstop.
//
// It returns every score rather than a top-k slice because the endpoint charges
// for all of them regardless (see RerankRequest): truncating here would discard
// measurements already paid for, and the ones below the caller's cut are exactly
// what a rerank floor would have to be calibrated against. Deciding how many to
// keep is the caller's job.
//
// The retry loop mirrors embedBatchWithRetry's shape and is deliberately written
// out rather than extracted into a shared helper: the two endpoints return
// different types, and generalizing would rewrite a working, already-evaluated
// embeddings path for no gain to this one.
func Rerank(query string, documents []string) ([]RerankResult, error) {
	if len(documents) == 0 {
		return nil, fmt.Errorf("rerank: no documents to score for query %q", query)
	}

	var lastErr error
	for attempt := 0; attempt <= VOYAGE_MAX_RETRIES; attempt++ {
		results, err := rerankBatch(query, documents)
		if err == nil {
			return results, nil
		}
		lastErr = err

		var rle *RateLimitError
		if !errors.As(err, &rle) {
			return nil, err // not a rate limit; fail fast
		}
		backoff := VOYAGE_RETRY_BACKOFF * time.Duration(attempt+1)
		log.Printf("voyage: rerank rate limited (attempt %d/%d), backing off %s", attempt+1, VOYAGE_MAX_RETRIES, backoff)
		time.Sleep(backoff)
	}
	return nil, fmt.Errorf("exhausted %d retries: %w", VOYAGE_MAX_RETRIES, lastErr)
}

// RerankMany scores each query against its own document list — the arm-4
// entrypoint, where one query is one story's title and its documents are that
// story's cosine-selected candidate chunks. Results come back one list per
// query, in query order, each carrying a score for EVERY document that query
// submitted (see Rerank).
//
// Unlike embeddings, this cannot be batched: a cross-encoder scores one (query,
// document set) pair per call, so N stories cost N requests. That is what makes
// the pacing here the dominant cost of an arm-4 run (~52s x N).
//
// The pacing is NOT removable by the fact that one call fits inside 10K tokens.
// TPM is a rate: at the 3 RPM floor alone, three calls a minute would offer
// ~23.6K tokens against a 10K budget, i.e. near-continuous 429s backing off
// 1min/2min/3min — slower than simply waiting. It is routed through pacingDelay
// rather than hardcoded as ~52s because the throttle is an account property:
// adding a payment method lifts it, and then raising VOYAGE_TPM_LIMIT speeds up
// both endpoints with no code change.
//
// Pacing lives here rather than in the caller for the same reason it lives in
// embedAll: the caller would have to know the endpoint's token budget to get it
// right, and would get it wrong in a second place. The sleep is applied BETWEEN
// calls and not after the last one — it protects the next request, and there
// isn't one. ⚠️ That last property is NOT covered by a test (asserting it needs
// a sleep seam that costs more than it buys); a stray trailing sleep would add
// ~52s x 12 batches ≈ 10 min to a ~5h eval, so it is cheap to get wrong and
// cheap to notice.
func RerankMany(queries []string, documents [][]string) ([][]RerankResult, error) {
	if len(queries) != len(documents) {
		return nil, fmt.Errorf("rerank: %d queries but %d document lists", len(queries), len(documents))
	}
	if len(queries) == 0 {
		return nil, nil
	}

	all := make([][]RerankResult, 0, len(queries))
	for i, query := range queries {
		log.Printf("voyage: reranking query %d/%d (%d documents, ~%d tokens)", i+1, len(queries), len(documents[i]), RERANK_TOKENS_PER_CALL)

		results, err := Rerank(query, documents[i])
		if err != nil {
			return nil, fmt.Errorf("rerank query %d/%d: %w", i+1, len(queries), err)
		}
		all = append(all, results)

		if i < len(queries)-1 {
			pace("rerank request", RERANK_TOKENS_PER_CALL)
		}
	}
	return all, nil
}
