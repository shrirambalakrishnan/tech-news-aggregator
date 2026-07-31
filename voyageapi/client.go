package voyageapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// This file is the TRANSPORT layer: the wire shapes of Voyage's /v1/embeddings
// endpoint and the single-request HTTP call. One batch in, one response out —
// it knows nothing about batching plans, pacing, or retries (ratelimit.go),
// except for flagging a 429 as a RateLimitError so the retry loop can react.

const (
	VOYAGE_EMBEDDINGS_URL      = "https://api.voyageai.com/v1/embeddings"
	VOYAGE_EMBEDDING_MODEL     = "voyage-4-lite"
	VOYAGE_INPUT_TYPE_DOCUMENT = "document"
	VOYAGE_INPUT_TYPE_QUERY    = "query"
)

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
