package voyageapi

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// noPacing zeroes the sleeps so rate-limit pacing/backoff don't slow tests, and
// restores them afterwards. One set of knobs covers both endpoints.
func noPacing(t *testing.T) {
	t.Helper()
	gap, backoff, safety := VOYAGE_MIN_REQUEST_GAP, VOYAGE_RETRY_BACKOFF, VOYAGE_RATE_SAFETY
	VOYAGE_MIN_REQUEST_GAP = 0
	VOYAGE_RETRY_BACKOFF = 0
	VOYAGE_RATE_SAFETY = 0 // zeroes the token-proportional pacing term too
	t.Cleanup(func() {
		VOYAGE_MIN_REQUEST_GAP = gap
		VOYAGE_RETRY_BACKOFF = backoff
		VOYAGE_RATE_SAFETY = safety
	})
}

// TestEmbedDocumentsBatching verifies EmbedDocuments respects the count ceiling
// and preserves input order across batch boundaries. Inputs are tiny so the
// token cap doesn't bind and VOYAGE_MAX_BATCH (128) governs.
func TestEmbedDocumentsBatching(t *testing.T) {
	noPacing(t)
	original := embedBatch
	defer func() { embedBatch = original }()

	var batchSizes []int
	var next float32
	embedBatch = func(texts []string, inputType string) ([][]float32, error) {
		if inputType != VOYAGE_INPUT_TYPE_DOCUMENT {
			t.Fatalf("expected input type %q, got %q", VOYAGE_INPUT_TYPE_DOCUMENT, inputType)
		}
		batchSizes = append(batchSizes, len(texts))
		out := make([][]float32, len(texts))
		for i := range texts {
			out[i] = []float32{next} // encode global position to verify ordering
			next++
		}
		return out, nil
	}

	texts := make([]string, 300)
	for i := range texts {
		texts[i] = fmt.Sprintf("doc-%d", i)
	}

	got, err := EmbedDocuments(texts)
	if err != nil {
		t.Fatalf("EmbedDocuments returned error: %v", err)
	}

	if len(got) != 300 {
		t.Fatalf("expected 300 embeddings, got %d", len(got))
	}
	if want := []int{128, 128, 44}; !reflect.DeepEqual(batchSizes, want) {
		t.Errorf("batch sizes = %v, want %v", batchSizes, want)
	}
	for k := range got {
		if got[k][0] != float32(k) {
			t.Fatalf("ordering broken: embedding %d = %v, want first element %d", k, got[k], k)
		}
	}
}

func TestEmbedDocumentsEmpty(t *testing.T) {
	original := embedBatch
	defer func() { embedBatch = original }()

	called := false
	embedBatch = func(texts []string, inputType string) ([][]float32, error) {
		called = true
		return nil, nil
	}

	got, err := EmbedDocuments(nil)
	if err != nil {
		t.Fatalf("EmbedDocuments(nil) returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 embeddings, got %d", len(got))
	}
	if called {
		t.Error("embedBatch should not be called for empty input")
	}
}

// TestEmbedQueriesUsesQueryInputType is the one thing that separates
// EmbedQueries from EmbedDocuments: the input type sent to the wire. Voyage
// embeds queries and documents differently, so a slip here would silently
// degrade retrieval rather than fail.
func TestEmbedQueriesUsesQueryInputType(t *testing.T) {
	noPacing(t)
	original := embedBatch
	defer func() { embedBatch = original }()

	var calls int
	var gotTexts []string
	var gotInputType string
	embedBatch = func(texts []string, inputType string) ([][]float32, error) {
		calls++
		gotTexts = texts
		gotInputType = inputType
		out := make([][]float32, len(texts))
		for i := range texts {
			out[i] = []float32{float32(i)} // encode position to verify ordering
		}
		return out, nil
	}

	titles := make([]string, 30) // a production-sized batch of story titles
	for i := range titles {
		titles[i] = fmt.Sprintf("story-%d", i)
	}

	got, err := EmbedQueries(titles)
	if err != nil {
		t.Fatalf("EmbedQueries returned error: %v", err)
	}
	if gotInputType != VOYAGE_INPUT_TYPE_QUERY {
		t.Errorf("expected input type %q, got %q", VOYAGE_INPUT_TYPE_QUERY, gotInputType)
	}
	// 30 short titles are far under both the token and count caps, so they
	// travel as one request — the point of embedding queries in bulk.
	if calls != 1 {
		t.Errorf("expected 30 titles to be one request, got %d", calls)
	}
	if !reflect.DeepEqual(gotTexts, titles) {
		t.Errorf("texts sent = %v, want %v", gotTexts, titles)
	}
	if len(got) != len(titles) {
		t.Fatalf("expected %d embeddings, got %d", len(titles), len(got))
	}
	for i := range got {
		if got[i][0] != float32(i) {
			t.Fatalf("ordering broken: embedding %d = %v", i, got[i])
		}
	}
}

func TestEmbedQueriesEmpty(t *testing.T) {
	original := embedBatch
	defer func() { embedBatch = original }()

	called := false
	embedBatch = func(texts []string, inputType string) ([][]float32, error) {
		called = true
		return nil, nil
	}

	got, err := EmbedQueries(nil)
	if err != nil {
		t.Fatalf("EmbedQueries(nil) returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 embeddings, got %d", len(got))
	}
	if called {
		t.Error("embedBatch should not be called for empty input")
	}
}

func TestPlanBatchesByTokens(t *testing.T) {
	// estimateTokens = len/4 + 1. A 16-char text => 5 tokens.
	a := "0123456789abcdef" // 16 chars -> 5 tokens
	texts := []string{a, a, a, a}
	// maxTokens 10 fits two 5-token texts per batch; high count ceiling.
	got := planBatches(texts, 10, 100)
	if len(got) != 2 || len(got[0]) != 2 || len(got[1]) != 2 {
		t.Fatalf("expected 2 batches of 2, got %v", got)
	}

	// A single oversized text becomes its own batch.
	big := make([]byte, 100)
	got = planBatches([]string{string(big), "x"}, 10, 100)
	if len(got) != 2 || len(got[0]) != 1 || got[0][0] != string(big) {
		t.Fatalf("oversized text should be its own batch, got %v", got)
	}
}

func TestPlanBatchesByCount(t *testing.T) {
	texts := []string{"a", "b", "c", "d", "e"}
	got := planBatches(texts, 1_000_000, 2) // token cap won't bind; count cap = 2
	if want := [][]string{{"a", "b"}, {"c", "d"}, {"e"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestEmbedDocumentsRetriesOnRateLimit(t *testing.T) {
	noPacing(t)
	original := embedBatch
	defer func() { embedBatch = original }()

	calls := 0
	embedBatch = func(texts []string, inputType string) ([][]float32, error) {
		calls++
		if calls == 1 {
			return nil, &RateLimitError{Status: "429 Too Many Requests", Body: "slow down"}
		}
		out := make([][]float32, len(texts))
		for i := range texts {
			out[i] = []float32{1}
		}
		return out, nil
	}

	got, err := EmbedDocuments([]string{"a", "b"})
	if err != nil {
		t.Fatalf("expected success after retry, got %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 embeddings, got %d", len(got))
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (1 rate-limited + 1 success), got %d", calls)
	}
}

func TestEmbedDocumentsFailsFastOnNonRateLimit(t *testing.T) {
	noPacing(t)
	original := embedBatch
	defer func() { embedBatch = original }()

	calls := 0
	embedBatch = func(texts []string, inputType string) ([][]float32, error) {
		calls++
		return nil, errors.New("boom")
	}

	if _, err := EmbedDocuments([]string{"a"}); err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("non-rate-limit error should not be retried; got %d calls", calls)
	}
}

func TestPacingDelayFloorIsRequestGap(t *testing.T) {
	gap := VOYAGE_MIN_REQUEST_GAP
	VOYAGE_MIN_REQUEST_GAP = 5 * time.Second
	defer func() { VOYAGE_MIN_REQUEST_GAP = gap }()

	if d := pacingDelay(1); d != 5*time.Second {
		t.Errorf("tiny batch should floor at the request gap, got %s", d)
	}
}

func TestEmbedQueryUsesQueryInputType(t *testing.T) {
	original := embedBatch
	defer func() { embedBatch = original }()

	var gotTexts []string
	var gotInputType string
	embedBatch = func(texts []string, inputType string) ([][]float32, error) {
		gotTexts = texts
		gotInputType = inputType
		return [][]float32{{0.1, 0.2}}, nil
	}

	vec, err := EmbedQuery("distributed systems")
	if err != nil {
		t.Fatalf("EmbedQuery returned error: %v", err)
	}
	if gotInputType != VOYAGE_INPUT_TYPE_QUERY {
		t.Errorf("expected input type %q, got %q", VOYAGE_INPUT_TYPE_QUERY, gotInputType)
	}
	if !reflect.DeepEqual(gotTexts, []string{"distributed systems"}) {
		t.Errorf("texts sent = %v", gotTexts)
	}
	if !reflect.DeepEqual(vec, []float32{0.1, 0.2}) {
		t.Errorf("vector = %v, want [0.1 0.2]", vec)
	}
}
