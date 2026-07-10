// Package voyageapi is a thin client for the Voyage AI embeddings API,
// mirroring claudeapi. It reads VOYAGE_API_KEY from the environment.
//
// The package is organized one file per layer:
//
//	voyage.go    — PUBLIC API: EmbedDocuments, the embedBatch DI seam
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

// EmbedDocuments embeds texts as retrieval documents, returning one vector per
// input text in the same order. It splits texts into token-bounded batches and
// paces requests to respect Voyage's free-tier rate limits (see ratelimit.go).
func EmbedDocuments(texts []string) ([][]float32, error) {
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

		log.Printf("voyage: embedding batch %d/%d (%d chunks, ~%d tokens)", i+1, len(batches), len(batch), batchTokens)
		vecs, err := embedBatchWithRetry(batch, VOYAGE_INPUT_TYPE_DOCUMENT)
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
