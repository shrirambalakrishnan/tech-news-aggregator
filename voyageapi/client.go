package voyageapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// This file is the TRANSPORT layer: the wire shapes of Voyage's /v1/embeddings
// and /v1/rerank endpoints and their single-request HTTP calls. One request in,
// one response out — it knows nothing about batching plans, pacing, or retries
// (ratelimit.go), except for flagging a 429 as a RateLimitError so the retry
// loop can react.

const (
	VOYAGE_EMBEDDINGS_URL      = "https://api.voyageai.com/v1/embeddings"
	VOYAGE_EMBEDDING_MODEL     = "voyage-4-lite"
	VOYAGE_INPUT_TYPE_DOCUMENT = "document"
	VOYAGE_INPUT_TYPE_QUERY    = "query"

	VOYAGE_RERANK_URL = "https://api.voyageai.com/v1/rerank"
	// VOYAGE_RERANK_MODEL is the cross-encoder arm 4 scores (query, chunk) pairs
	// with. Voyage's valid ids are rerank-2.5, rerank-2.5-lite, rerank-2,
	// rerank-2-lite, rerank-1 and rerank-lite-1 — "rerank2.5" is not one of
	// them. Changing it re-bases every arm-4 number measured under the old one.
	VOYAGE_RERANK_MODEL = "rerank-2.5"
)

// VOYAGE_RERANK_TIMEOUT bounds a single /v1/rerank round trip. Duration, well
// above a normal response (a handful of seconds) and well below the pacing gap.
//
// It exists because an arm-4 eval makes ~341 sequential calls over ~5 hours
// whose NORMAL behaviour is long silence: without a timeout, a hung connection
// is indistinguishable from pacing and would stall the run indefinitely. The
// embed path is left on the default (no timeout) — this is additive, not a
// change to an already-evaluated path.
var VOYAGE_RERANK_TIMEOUT = 2 * time.Minute

type Request struct {
	Model     string   `json:"model"`
	Input     []string `json:"input"`
	InputType string   `json:"input_type"`
}

type EmbeddingData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type Response struct {
	Object string          `json:"object"`
	Data   []EmbeddingData `json:"data"`
	Model  string          `json:"model"`
	Usage  struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// RerankRequest is the wire shape of POST /v1/rerank. ReturnDocuments is sent
// false: results are mapped back to chunks by Index, so echoing the document
// text back would triple the response for nothing.
type RerankRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopK            int      `json:"top_k,omitempty"`
	ReturnDocuments bool     `json:"return_documents"`
}

// RerankData is one scored document. Index is its position in the SUBMITTED
// documents slice — the only thing tying a score back to the chunk it belongs
// to, since the API returns results reordered and truncated to top_k.
type RerankData struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

type RerankResponse struct {
	Object string       `json:"object"`
	Data   []RerankData `json:"data"`
	Model  string       `json:"model"`
	Usage  struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// rerankHTTP scores documents against query in a single HTTP request, returning
// at most topK results. Voyage returns them already sorted by descending
// relevance score; that order is preserved rather than re-sorted here, since
// re-sorting would paper over a change in the API's contract instead of letting
// it surface.
func rerankHTTP(query string, documents []string, topK int) ([]RerankResult, error) {
	reqBody := RerankRequest{
		Model:           VOYAGE_RERANK_MODEL,
		Query:           query,
		Documents:       documents,
		TopK:            topK,
		ReturnDocuments: false,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal error: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, VOYAGE_RERANK_URL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("request error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("VOYAGE_API_KEY"))

	client := &http.Client{Timeout: VOYAGE_RERANK_TIMEOUT}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		body, _ := io.ReadAll(resp.Body)
		return nil, &RateLimitError{Status: resp.Status, Body: string(body)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %s: %s", resp.Status, string(body))
	}

	var response RerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("decode error: %w", err)
	}
	return mapRerankData(response.Data, len(documents))
}

// mapRerankData converts the wire results to RerankResults, rejecting an index
// that does not address a submitted document.
//
// The bounds check mirrors embedBatchHTTP's: the index is what the caller
// indexes its own candidate slice with, so an out-of-range value has to become
// an error here rather than a panic there. Split out of rerankHTTP so it is
// testable without a fake HTTP server.
func mapRerankData(data []RerankData, documentCount int) ([]RerankResult, error) {
	results := make([]RerankResult, 0, len(data))
	for _, d := range data {
		if d.Index < 0 || d.Index >= documentCount {
			return nil, fmt.Errorf("rerank index %d out of range for %d documents", d.Index, documentCount)
		}
		results = append(results, RerankResult{Index: d.Index, RelevanceScore: d.RelevanceScore})
	}
	return results, nil
}

// embedBatchHTTP embeds one batch of texts in a single HTTP request, returning
// one vector per text in input order.
func embedBatchHTTP(texts []string, inputType string) ([][]float32, error) {
	reqBody := Request{
		Model:     VOYAGE_EMBEDDING_MODEL,
		Input:     texts,
		InputType: inputType,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal error: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, VOYAGE_EMBEDDINGS_URL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("request error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("VOYAGE_API_KEY"))

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		body, _ := io.ReadAll(resp.Body)
		return nil, &RateLimitError{Status: resp.Status, Body: string(body)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %s: %s", resp.Status, string(body))
	}

	var response Response
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("decode error: %w", err)
	}

	// Voyage tags each embedding with its input index; reorder defensively so
	// the returned slice matches input order regardless of response ordering.
	embeddings := make([][]float32, len(texts))
	for _, d := range response.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("embedding index %d out of range for batch of %d", d.Index, len(texts))
		}
		embeddings[d.Index] = d.Embedding
	}
	for i, e := range embeddings {
		if e == nil {
			return nil, fmt.Errorf("missing embedding for index %d in batch of %d", i, len(texts))
		}
	}
	return embeddings, nil
}
