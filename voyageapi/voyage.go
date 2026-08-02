// Package voyageapi is a thin client for the Voyage AI embeddings API,
// mirroring claudeapi. It reads VOYAGE_API_KEY from the environment.
//
// The package is organized one file per layer:
//
//	voyage.go    — PUBLIC API: EmbedDocuments/EmbedQueries, the embedBatch DI seam
//	ratelimit.go — POLICY: free-tier batching, pacing, and retry rules
//	client.go    — TRANSPORT: wire types + the single-request HTTP call
package voyageapi

import (
	"fmt"
	"log"
	"time"
)

// embedBatch is the network seam (function-variable DI): it embeds a single
// batch in one HTTP call. Routed through a package var so tests can swap it for
// a fake and exercise EmbedDocuments' batching, pacing, and retry logic without
// hitting the network.
var embedBatch = embedBatchHTTP

// EmbedQuery embeds a single retrieval query. Voyage optimizes embeddings
// differently for the two sides of retrieval (input_type "query" vs
// "document"), so queries must not be embedded with EmbedDocuments. A single
// query is one small request — no batching or pacing needed — but it shares
// the 429 retry backstop.
func EmbedQuery(text string) ([]float32, error) {
	vecs, err := embedBatchWithRetry([]string{text}, VOYAGE_INPUT_TYPE_QUERY)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	return vecs[0], nil
}

// EmbedDocuments embeds texts as retrieval documents, returning one vector per
// input text in the same order. It splits texts into token-bounded batches and
// paces requests to respect Voyage's free-tier rate limits (see ratelimit.go).
func EmbedDocuments(texts []string) ([][]float32, error) {
	return embedAll(texts, VOYAGE_INPUT_TYPE_DOCUMENT)
}

// EmbedQueries embeds texts as retrieval queries (the plural of EmbedQuery),
// returning one vector per input text in the same order. Arm 3 retrieves per
// story, so it needs a vector per story title; since Voyage's request body
// takes a list, N titles cost one HTTP request, not N.
//
// Batching and pacing are shared with EmbedDocuments — only input_type differs,
// and that difference matters: Voyage embeds the two sides of retrieval
// differently, so query texts must not go through EmbedDocuments.
func EmbedQueries(texts []string) ([][]float32, error) {
	return embedAll(texts, VOYAGE_INPUT_TYPE_QUERY)
}

// embedAll is the shared body of EmbedDocuments and EmbedQueries: token-bounded
// batching, free-tier pacing between requests, and per-batch 429 retry.
func embedAll(texts []string, inputType string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	batches := planBatches(texts, VOYAGE_MAX_TOKENS_PER_BATCH, VOYAGE_MAX_BATCH)
	embeddings := make([][]float32, 0, len(texts))

	for i, batch := range batches {
		batchTokens := 0
		for _, t := range batch {
			batchTokens += estimateTokens(t)
		}

		log.Printf("voyage: embedding %s batch %d/%d (%d texts, ~%d tokens)", inputType, i+1, len(batches), len(batch), batchTokens)
		vecs, err := embedBatchWithRetry(batch, inputType)
		if err != nil {
			return nil, fmt.Errorf("embed batch %d/%d: %w", i+1, len(batches), err)
		}
		embeddings = append(embeddings, vecs...)

		// Pace between batches, not after the last one — the sleep protects the
		// *next* request, and there isn't one.
		if i < len(batches)-1 {
			time.Sleep(pacingDelay(batchTokens))
		}
	}
	return embeddings, nil
}
