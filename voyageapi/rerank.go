package voyageapi

import (
	"fmt"
	"log"
)

// This file is the PUBLIC API of the rerank endpoint, the second half of a
// two-stage retrieval: embeddings narrow the corpus by cosine similarity, then a
// cross-encoder rescores the survivors.
//
// The difference that makes it worth a second network call: an embedding is
// computed once, before any query exists, so a chunk's vector is a topical
// average of its 800 words. A cross-encoder reads the query and the document
// together in one forward pass, so relevance living in one paragraph is not
// averaged away by the other seven. The cost is that nothing is precomputable —
// a cross-encoder emits no reusable vector, so it must run live for every
// (query, document) pair, which is why it runs over a handful of candidates and
// never the whole index.

// RerankResult is one scored document. Index is its position in the SUBMITTED
// documents slice, which is what maps a score back to the chunk it came from:
// the API returns results reordered by score and truncated to topK, so position
// in the response says nothing about which document was scored.
type RerankResult struct {
	Index          int
	RelevanceScore float64
}

// rerankBatch is the network seam (function-variable DI): one query and its
// documents in one HTTP call. Routed through a package var so tests can exercise
// pacing, retry and result mapping without hitting the network.
var rerankBatch = rerankHTTP

// Rerank scores documents against a single query, returning at most topK
// results, best first. One query, one request; pacing between requests is
// RerankMany's job, so a lone call pays no delay - only the 429 retry backstop.
func Rerank(query string, documents []string, topK int) ([]RerankResult, error) {
	if len(documents) == 0 {
		return nil, fmt.Errorf("rerank: no documents to score for query %q", query)
	}

	var results []RerankResult
	err := retryOn429(func() error {
		var err error
		results, err = rerankBatch(query, documents, topK)
		return err
	}, VOYAGE_MAX_RETRIES, VOYAGE_RETRY_BACKOFF)
	if err != nil {
		return nil, err
	}
	return results, nil
}

// RerankMany scores each query against its own document list - the arm-4
// entrypoint, where one query is one story's title and its documents are that
// story's cosine-selected candidate chunks. Results come back one list per
// query, in query order.
//
// Unlike embeddings, this cannot be batched: a cross-encoder scores one (query,
// document set) pair per call, so N stories cost N requests. That is what makes
// the pacing here the dominant cost of an arm-4 run.
//
// Pacing lives here rather than in the caller for the same reason it lives in
// embedAll: the caller would have to know the endpoint's token budget to get it
// right, and would get it wrong in a second place. The sleep is applied BETWEEN
// calls and not after the last one - it protects the next request, and there
// isn't one.
func RerankMany(queries []string, documents [][]string, topK int) ([][]RerankResult, error) {
	if len(queries) != len(documents) {
		return nil, fmt.Errorf("rerank: %d queries but %d document lists", len(queries), len(documents))
	}
	if len(queries) == 0 {
		return nil, nil
	}

	all := make([][]RerankResult, 0, len(queries))
	for i, query := range queries {
		tokens := rerankTokens(query, documents[i])
		log.Printf("voyage: reranking query %d/%d (%d documents, ~%d tokens)", i+1, len(queries), len(documents[i]), tokens)

		results, err := Rerank(query, documents[i], topK)
		if err != nil {
			return nil, fmt.Errorf("rerank query %d/%d: %w", i+1, len(queries), err)
		}
		all = append(all, results)

		if i < len(queries)-1 {
			sleep(pacingDelay(tokens))
		}
	}
	return all, nil
}
