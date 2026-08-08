package voyageapi

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// stubRerank swaps the network seam and restores it afterwards.
func stubRerank(t *testing.T, fn func(query string, documents []string, topK int) ([]RerankResult, error)) {
	t.Helper()
	original := rerankBatch
	rerankBatch = fn
	t.Cleanup(func() { rerankBatch = original })
}

// TestRerankMapsResultsBackByIndex is the point of RerankResult.Index: Voyage
// returns results reordered by score and truncated to top_k, so position in the
// response says nothing about which document was scored. If the index were
// dropped or renumbered, the caller would silently attach every score to the
// wrong chunk - retrieval would still "work" and would be quietly meaningless.
func TestRerankMapsResultsBackByIndex(t *testing.T) {
	noPacing(t)
	stubRerank(t, func(query string, documents []string, topK int) ([]RerankResult, error) {
		// The 3rd and 1st submitted documents win, in that order.
		return []RerankResult{
			{Index: 2, RelevanceScore: 0.91},
			{Index: 0, RelevanceScore: 0.42},
		}, nil
	})

	docs := []string{"raft", "spanner", "wal"}
	got, err := Rerank("consensus protocols", docs, 2)
	if err != nil {
		t.Fatalf("Rerank returned error: %v", err)
	}

	want := []RerankResult{{Index: 2, RelevanceScore: 0.91}, {Index: 0, RelevanceScore: 0.42}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("results = %+v, want %+v", got, want)
	}
	if docs[got[0].Index] != "wal" {
		t.Errorf("index %d resolves to %q, want wal", got[0].Index, docs[got[0].Index])
	}
}

// TestRerankSendsTopKAndModel pins what reaches the wire. The model id is
// recorded on every arm-4 run record, so sending a different one than we record
// would make the record a lie.
func TestRerankSendsTopKAndModel(t *testing.T) {
	noPacing(t)

	var gotQuery string
	var gotDocs []string
	var gotTopK int
	stubRerank(t, func(query string, documents []string, topK int) ([]RerankResult, error) {
		gotQuery, gotDocs, gotTopK = query, documents, topK
		return []RerankResult{{Index: 0, RelevanceScore: 1}}, nil
	})

	if _, err := Rerank("a title", []string{"x", "y"}, 2); err != nil {
		t.Fatalf("Rerank returned error: %v", err)
	}
	if gotQuery != "a title" {
		t.Errorf("query = %q, want %q", gotQuery, "a title")
	}
	if !reflect.DeepEqual(gotDocs, []string{"x", "y"}) {
		t.Errorf("documents = %v", gotDocs)
	}
	if gotTopK != 2 {
		t.Errorf("top_k = %d, want 2", gotTopK)
	}

	// The transport layer is what stamps the model; assert the constant is the
	// id Voyage actually accepts (the spec said "rerank2.5", which is not one).
	if VOYAGE_RERANK_MODEL != "rerank-2.5" {
		t.Errorf("VOYAGE_RERANK_MODEL = %q, want a valid Voyage rerank id", VOYAGE_RERANK_MODEL)
	}
}

func TestRerankRejectsEmptyDocuments(t *testing.T) {
	called := false
	stubRerank(t, func(query string, documents []string, topK int) ([]RerankResult, error) {
		called = true
		return nil, nil
	})

	if _, err := Rerank("a title", nil, 2); err == nil {
		t.Fatal("expected an error for an empty document list, got nil")
	}
	if called {
		t.Error("an empty document list must not reach the network")
	}
}

// recordSleeps swaps the sleep seam for a recorder, so pacing is asserted from
// the durations requested rather than by waiting for them.
func recordSleeps(t *testing.T) *[]time.Duration {
	t.Helper()
	original := sleep
	var slept []time.Duration
	sleep = func(d time.Duration) { slept = append(slept, d) }
	t.Cleanup(func() { sleep = original })
	return &slept
}

// TestRerankManyPacesBetweenCallsOnly: the sleep protects the NEXT request, so
// there must not be one after the last. At ~52s a call, a stray trailing sleep
// is ~5 minutes wasted across a 341-story eval.
func TestRerankManyPacesBetweenCallsOnly(t *testing.T) {
	slept := recordSleeps(t)

	stubRerank(t, func(query string, documents []string, topK int) ([]RerankResult, error) {
		return []RerankResult{{Index: 0, RelevanceScore: 1}}, nil
	})

	queries := []string{"one", "two", "three"}
	docs := [][]string{{"a"}, {"b"}, {"c"}}

	got, err := RerankMany(queries, docs, 1)
	if err != nil {
		t.Fatalf("RerankMany returned error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 result lists, got %d", len(got))
	}
	// Two gaps for three calls, never three.
	if len(*slept) != 2 {
		t.Errorf("slept %d times for 3 calls, want 2 (between calls, none after the last): %v", len(*slept), *slept)
	}
	for _, d := range *slept {
		if d != rerankPacingDelay(rerankTokens("one", []string{"a"})) {
			t.Errorf("paced %s, want the rerank endpoint's delay for the request just sent", d)
		}
	}
}

// TestRerankManyPacesOnTheRerankBudget: the delay must come from the rerank
// constants, not the embeddings ones. They happen to hold the same numbers
// today, so only a test that moves one of them can tell which was read.
func TestRerankManyPacesOnTheRerankBudget(t *testing.T) {
	slept := recordSleeps(t)

	gap := VOYAGE_RERANK_MIN_REQUEST_GAP
	VOYAGE_RERANK_MIN_REQUEST_GAP = 99 * time.Second
	defer func() { VOYAGE_RERANK_MIN_REQUEST_GAP = gap }()

	stubRerank(t, func(query string, documents []string, topK int) ([]RerankResult, error) {
		return []RerankResult{{Index: 0, RelevanceScore: 1}}, nil
	})

	if _, err := RerankMany([]string{"one", "two"}, [][]string{{"a"}, {"b"}}, 1); err != nil {
		t.Fatalf("RerankMany returned error: %v", err)
	}
	if len(*slept) != 1 || (*slept)[0] != 99*time.Second {
		t.Errorf("slept %v, want one 99s gap from the rerank floor", *slept)
	}
}

func TestRerankManyRejectsLengthMismatch(t *testing.T) {
	called := false
	stubRerank(t, func(query string, documents []string, topK int) ([]RerankResult, error) {
		called = true
		return nil, nil
	})

	if _, err := RerankMany([]string{"a", "b"}, [][]string{{"x"}}, 1); err == nil {
		t.Fatal("expected an error when queries and document lists differ in length")
	}
	if called {
		t.Error("a length mismatch must not reach the network")
	}
}

func TestRerankRetriesOnRateLimit(t *testing.T) {
	// The default schedule is 1min/2min/3min, which no test can afford to
	// observe, so the sleeps are zeroed and the growth is asserted separately in
	// TestRetryOn429BacksOffLinearly against the shared helper.
	defaultBackoff := VOYAGE_RERANK_RETRY_BACKOFF
	noPacing(t)

	calls := 0
	stubRerank(t, func(query string, documents []string, topK int) ([]RerankResult, error) {
		calls++
		if calls <= 3 {
			return nil, &RateLimitError{Status: "429 Too Many Requests", Body: "slow down"}
		}
		return []RerankResult{{Index: 0, RelevanceScore: 1}}, nil
	})

	got, err := Rerank("a title", []string{"x"}, 1)
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 result, got %d", len(got))
	}
	if calls != 4 {
		t.Errorf("expected 4 calls (3 rate-limited + 1 success), got %d", calls)
	}

	// The shipped unit is the issue's 1 minute, deliberately different from the
	// embeddings endpoint's 30s.
	if defaultBackoff != time.Minute {
		t.Errorf("VOYAGE_RERANK_RETRY_BACKOFF = %s, want 1m", defaultBackoff)
	}
}

// TestRetryOn429BacksOffLinearly pins the schedule both endpoints share: the nth
// retry waits n times the unit — 1min, 2min, 3min at the rerank endpoint's
// shipped 60s. Asserted from the durations requested, so it costs no wall clock.
func TestRetryOn429BacksOffLinearly(t *testing.T) {
	slept := recordSleeps(t)

	err := retryOn429(func() error {
		return &RateLimitError{Status: "429", Body: "no"}
	}, 2, time.Minute)

	if err == nil {
		t.Fatal("expected an error once retries are exhausted")
	}
	want := []time.Duration{time.Minute, 2 * time.Minute, 3 * time.Minute}
	if !reflect.DeepEqual(*slept, want) {
		t.Errorf("backoff schedule = %v, want linear growth %v", *slept, want)
	}
}

func TestRerankExhaustsRetries(t *testing.T) {
	retries, backoff := VOYAGE_RERANK_MAX_RETRIES, VOYAGE_RERANK_RETRY_BACKOFF
	VOYAGE_RERANK_MAX_RETRIES = 2
	VOYAGE_RERANK_RETRY_BACKOFF = 0
	defer func() { VOYAGE_RERANK_MAX_RETRIES, VOYAGE_RERANK_RETRY_BACKOFF = retries, backoff }()

	calls := 0
	stubRerank(t, func(query string, documents []string, topK int) ([]RerankResult, error) {
		calls++
		return nil, &RateLimitError{Status: "429", Body: "no"}
	})

	if _, err := Rerank("a title", []string{"x"}, 1); err == nil {
		t.Fatal("expected an error once retries are exhausted")
	}
	if calls != 3 { // attempt 0 plus 2 retries
		t.Errorf("expected 3 attempts, got %d", calls)
	}
}

func TestRerankFailsFastOnNonRateLimit(t *testing.T) {
	noPacing(t)

	calls := 0
	stubRerank(t, func(query string, documents []string, topK int) ([]RerankResult, error) {
		calls++
		return nil, errors.New("boom")
	})

	if _, err := Rerank("a title", []string{"x"}, 1); err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("non-rate-limit error should not be retried; got %d calls", calls)
	}
}

// TestMapRerankDataRejectsOutOfRangeIndex covers the transport layer's bounds
// check. The index is what the caller indexes its candidate slice with, so an
// out-of-range value has to become an error here rather than a panic there.
func TestMapRerankDataRejectsOutOfRangeIndex(t *testing.T) {
	for _, index := range []int{-1, 2, 5} {
		t.Run(fmt.Sprintf("index %d", index), func(t *testing.T) {
			if _, err := mapRerankData([]RerankData{{Index: index, RelevanceScore: 1}}, 2); err == nil {
				t.Errorf("index %d should be rejected for 2 documents", index)
			}
		})
	}
}

// TestMapRerankDataPreservesAPIOrder: Voyage returns results already sorted by
// descending score, and that order is carried through rather than re-sorted, so
// a change in the API's contract surfaces instead of being papered over.
func TestMapRerankDataPreservesAPIOrder(t *testing.T) {
	got, err := mapRerankData([]RerankData{
		{Index: 2, RelevanceScore: 0.9},
		{Index: 0, RelevanceScore: 0.4},
	}, 3)
	if err != nil {
		t.Fatalf("mapRerankData returned error: %v", err)
	}
	want := []RerankResult{{Index: 2, RelevanceScore: 0.9}, {Index: 0, RelevanceScore: 0.4}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("results = %+v, want %+v", got, want)
	}
}

func TestRerankTokensCountsQueryAndDocuments(t *testing.T) {
	query := "0123456789abcdef"   // 16 chars -> 5 tokens
	doc := "0123456789abcdefghij" // 20 chars -> 6 tokens

	if got, want := rerankTokens(query, []string{doc, doc}), 5+6+6; got != want {
		t.Errorf("rerankTokens = %d, want %d", got, want)
	}
}

// TestRerankPacingDelayMatchesTheIssuesFigure checks the ~52s the arm's runtime
// estimate is built on: 6 chunks of ~800 words is ~7.9K tokens, 79% of the
// assumed 10K/min budget.
func TestRerankPacingDelayMatchesTheIssuesFigure(t *testing.T) {
	got := rerankPacingDelay(7859)
	if got < 50*time.Second || got > 55*time.Second {
		t.Errorf("rerankPacingDelay(7859) = %s, want ~52s (the figure the ~5h eval estimate assumes)", got)
	}
}

func TestRerankPacingDelayFloorIsRequestGap(t *testing.T) {
	gap := VOYAGE_RERANK_MIN_REQUEST_GAP
	VOYAGE_RERANK_MIN_REQUEST_GAP = 5 * time.Second
	defer func() { VOYAGE_RERANK_MIN_REQUEST_GAP = gap }()

	if d := rerankPacingDelay(1); d != 5*time.Second {
		t.Errorf("a tiny request should floor at the request gap, got %s", d)
	}
}
