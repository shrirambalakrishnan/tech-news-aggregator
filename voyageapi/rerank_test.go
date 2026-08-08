package voyageapi

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// stubRerank swaps the network seam for the duration of the test.
func stubRerank(t *testing.T, fake func(query string, documents []string, topK int) ([]RerankResult, error)) {
	t.Helper()
	original := rerankBatch
	rerankBatch = fake
	t.Cleanup(func() { rerankBatch = original })
}

// TestRerankMapsResultsBackToSubmittedDocuments is the load-bearing test of the
// whole endpoint: Voyage returns results reordered by score and truncated to
// top_k, so the ONLY thing tying a score to the chunk it belongs to is Index.
// Get this wrong and every score is silently attached to the wrong chunk while
// the run completes and scores normally.
func TestRerankMapsResultsBackToSubmittedDocuments(t *testing.T) {
	noPacing(t)
	documents := []string{"doc-0", "doc-1", "doc-2", "doc-3"}

	stubRerank(t, func(query string, docs []string, topK int) ([]RerankResult, error) {
		// The third submitted document is the best match, the first the worst.
		return []RerankResult{
			{Index: 2, RelevanceScore: 0.91},
			{Index: 0, RelevanceScore: 0.12},
		}, nil
	})

	results, err := Rerank("a story title", documents, 2)
	if err != nil {
		t.Fatalf("Rerank returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if documents[results[0].Index] != "doc-2" {
		t.Fatalf("best result maps to %q, want doc-2", documents[results[0].Index])
	}
	if documents[results[1].Index] != "doc-0" {
		t.Fatalf("second result maps to %q, want doc-0", documents[results[1].Index])
	}
	if results[0].RelevanceScore <= results[1].RelevanceScore {
		t.Fatalf("expected descending order, got %v then %v", results[0].RelevanceScore, results[1].RelevanceScore)
	}
}

// TestRerankRequestBodyCarriesModelQueryAndTopK pins what actually reaches the
// wire. The model id is the one the plan had to correct ("rerank2.5" is not a
// valid Voyage id), and top_k is what keeps each story to two chunks.
func TestRerankRequestBodyCarriesModelQueryAndTopK(t *testing.T) {
	body := RerankRequest{
		Model:           VOYAGE_RERANK_MODEL,
		Query:           "Show HN: a thing",
		Documents:       []string{"a", "b"},
		TopK:            2,
		ReturnDocuments: false,
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded["model"] != "rerank-2.5" {
		t.Fatalf("model = %v, want rerank-2.5", decoded["model"])
	}
	if decoded["query"] != "Show HN: a thing" {
		t.Fatalf("query = %v", decoded["query"])
	}
	if decoded["top_k"] != float64(2) {
		t.Fatalf("top_k = %v, want 2", decoded["top_k"])
	}
	// Documents are mapped back by index, so echoing their text would triple
	// the response for nothing.
	if decoded["return_documents"] != false {
		t.Fatalf("return_documents = %v, want false", decoded["return_documents"])
	}
}

// TestRerankRejectsEmptyDocuments checks the guard fires BEFORE the network: an
// empty document list is a request the reranker cannot answer, and sending one
// would pay a ~52s pacing delay for nothing.
func TestRerankRejectsEmptyDocuments(t *testing.T) {
	noPacing(t)
	called := false
	stubRerank(t, func(query string, docs []string, topK int) ([]RerankResult, error) {
		called = true
		return nil, nil
	})

	if _, err := Rerank("title", nil, 2); err == nil {
		t.Fatal("expected an error for an empty document list, got nil")
	}
	if called {
		t.Fatal("Rerank hit the network for an empty document list")
	}
}

// TestRerankManyRejectsLengthMismatch: queries and document lists are positional
// partners, so a mismatch means some story's chunks would be scored against
// another story's title.
func TestRerankManyRejectsLengthMismatch(t *testing.T) {
	noPacing(t)
	called := false
	stubRerank(t, func(query string, docs []string, topK int) ([]RerankResult, error) {
		called = true
		return nil, nil
	})

	if _, err := RerankMany([]string{"a", "b"}, [][]string{{"doc"}}, 2); err == nil {
		t.Fatal("expected an error for 2 queries and 1 document list, got nil")
	}
	if called {
		t.Fatal("RerankMany hit the network despite a length mismatch")
	}
}

// TestRerankManyReturnsOneResultListPerQueryInOrder: the caller maps result list
// i back to story i, so order is part of the contract.
func TestRerankManyReturnsOneResultListPerQueryInOrder(t *testing.T) {
	noPacing(t)
	var seen []string
	stubRerank(t, func(query string, docs []string, topK int) ([]RerankResult, error) {
		seen = append(seen, query)
		return []RerankResult{{Index: 0, RelevanceScore: float64(len(seen))}}, nil
	})

	results, err := RerankMany([]string{"q0", "q1", "q2"}, [][]string{{"a"}, {"b"}, {"c"}}, 2)
	if err != nil {
		t.Fatalf("RerankMany returned error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 result lists, got %d", len(results))
	}
	for i, want := range []float64{1, 2, 3} {
		if results[i][0].RelevanceScore != want {
			t.Fatalf("result %d has score %v, want %v (queries scored out of order)", i, results[i][0].RelevanceScore, want)
		}
	}
	if len(seen) != 3 || seen[0] != "q0" || seen[2] != "q2" {
		t.Fatalf("queries reached the wire as %v", seen)
	}
}

// TestRerankRetriesOn429ThenSucceeds: a 429 is transient by definition (wait
// long enough and the budget refills), so it is the one error worth retrying.
func TestRerankRetriesOn429ThenSucceeds(t *testing.T) {
	noPacing(t)
	attempts := 0
	stubRerank(t, func(query string, docs []string, topK int) ([]RerankResult, error) {
		attempts++
		if attempts < 3 {
			return nil, &RateLimitError{Status: "429 Too Many Requests", Body: "slow down"}
		}
		return []RerankResult{{Index: 0, RelevanceScore: 0.5}}, nil
	})

	results, err := Rerank("title", []string{"doc"}, 1)
	if err != nil {
		t.Fatalf("Rerank returned error: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

// TestRerankExhaustsRetries: retries are bounded, so a persistent 429 becomes an
// error rather than an infinite loop.
func TestRerankExhaustsRetries(t *testing.T) {
	noPacing(t)
	attempts := 0
	stubRerank(t, func(query string, docs []string, topK int) ([]RerankResult, error) {
		attempts++
		return nil, &RateLimitError{Status: "429 Too Many Requests", Body: "slow down"}
	})

	if _, err := Rerank("title", []string{"doc"}, 1); err == nil {
		t.Fatal("expected an error after exhausting retries, got nil")
	}
	if attempts != VOYAGE_MAX_RETRIES+1 {
		t.Fatalf("expected %d attempts, got %d", VOYAGE_MAX_RETRIES+1, attempts)
	}
}

// TestRerankFailsFastOnNonRateLimitError: retrying a malformed request or a bad
// key just wastes minutes — nothing about waiting changes the outcome.
func TestRerankFailsFastOnNonRateLimitError(t *testing.T) {
	noPacing(t)
	attempts := 0
	boom := errors.New("unexpected status 400 Bad Request")
	stubRerank(t, func(query string, docs []string, topK int) ([]RerankResult, error) {
		attempts++
		return nil, boom
	})

	_, err := Rerank("title", []string{"doc"}, 1)
	if !errors.Is(err, boom) {
		t.Fatalf("expected the underlying error back, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
}

// TestMapRerankDataRejectsOutOfRangeIndex: the caller uses Index to subscript
// its own candidate slice, so an out-of-range value must become an error here
// rather than a panic there.
func TestMapRerankDataRejectsOutOfRangeIndex(t *testing.T) {
	for _, index := range []int{-1, 3, 99} {
		if _, err := mapRerankData([]RerankData{{Index: index, RelevanceScore: 0.5}}, 3); err == nil {
			t.Fatalf("index %d for 3 documents: expected an error, got nil", index)
		}
	}
}

// TestMapRerankDataPreservesResponseOrder: Voyage documents results as already
// sorted by descending score. Re-sorting here would paper over a change in that
// contract; preserving it lets the change surface.
func TestMapRerankDataPreservesResponseOrder(t *testing.T) {
	results, err := mapRerankData([]RerankData{
		{Index: 2, RelevanceScore: 0.9},
		{Index: 0, RelevanceScore: 0.4},
		{Index: 1, RelevanceScore: 0.1},
	}, 3)
	if err != nil {
		t.Fatalf("mapRerankData returned error: %v", err)
	}

	for i, want := range []int{2, 0, 1} {
		if results[i].Index != want {
			t.Fatalf("result %d has index %d, want %d (response order not preserved)", i, results[i].Index, want)
		}
	}
}

// TestRerankPacingIsAboutFiftyTwoSeconds pins the number the ~5h arm-4 runtime
// is made of. It deliberately does NOT call noPacing — this is the real formula
// at the shipped constants, as a pure function.
//
// Under-pacing does not save time: at 3 calls/min the account would be offered
// ~23.6K tokens against a 10K budget, i.e. near-continuous 429s backing off
// 1min/2min/3min, which is slower than waiting.
func TestRerankPacingIsAboutFiftyTwoSeconds(t *testing.T) {
	got := pacingDelay(RERANK_TOKENS_PER_CALL)
	if got < 50*time.Second || got > 54*time.Second {
		t.Fatalf("pacingDelay(%d) = %s, want ~52s", RERANK_TOKENS_PER_CALL, got)
	}
}

// TestRerankTokensPerCallFitsOneMinuteBudget: the constant is what pacing is
// computed from, so it has to stay inside the per-minute budget. If
// rag.RERANK_CANDIDATES_PER_STORY ever rises without this rising with it, the
// pacing under-counts and the run buys 429s instead of throughput.
func TestRerankTokensPerCallFitsOneMinuteBudget(t *testing.T) {
	if RERANK_TOKENS_PER_CALL > VOYAGE_TPM_LIMIT {
		t.Fatalf("RERANK_TOKENS_PER_CALL (%d) exceeds VOYAGE_TPM_LIMIT (%d): one call cannot fit in a minute's budget",
			RERANK_TOKENS_PER_CALL, VOYAGE_TPM_LIMIT)
	}
}
