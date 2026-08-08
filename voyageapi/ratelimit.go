package voyageapi

import (
	"errors"
	"fmt"
	"log"
	"time"
)

// This file is the RATE-LIMIT POLICY layer: everything that exists only
// because of Voyage's no-payment tier limits (3 requests/min, 10K tokens/min).
// Three cooperating defenses, cheapest first:
//
//  1. planBatches   — split the work so no single request blows the budget
//  2. pacingDelay   — sleep between requests so the per-minute rates hold
//  3. retry + backoff (retryOn429) — the backstop when 1+2 still land a 429,
//     since token counts are only estimated
//
// ONE set of constants covers BOTH endpoints, embeddings and rerank, because
// the throttle is the ACCOUNT's rather than the endpoint's: 3 RPM / 10K TPM was
// measured on embeddings and confirmed by probe on /v1/rerank at the shape arm 4
// sends (6 chunks + 1 title), where the paced ~52s behaved as predicted. Same
// limits, same confidence — so a parallel VOYAGE_RERANK_* set would be four
// constants holding these same values, and a second place to forget when the
// account tier changes.
//
// The 200M free-token allowance still applies, so staying under these limits
// keeps the whole corpus embed ~free. These are mutable package vars so tests
// can zero the sleeps. Adding a payment method on the Voyage dashboard lifts
// the throttle — then these conservative values just make the run a little
// slower than necessary, nothing breaks.
var (
	VOYAGE_TPM_LIMIT            = 10000            // tokens per minute (free tier)
	VOYAGE_MAX_TOKENS_PER_BATCH = 8000             // keep one request under the per-minute budget
	VOYAGE_MAX_BATCH            = 128              // hard ceiling on texts per request (Voyage caps at 1000)
	VOYAGE_MIN_REQUEST_GAP      = 20 * time.Second // >= this between requests => <= 3 RPM
	VOYAGE_RATE_SAFETY          = 1.10             // pad token-paced sleeps for estimate error
	VOYAGE_MAX_RETRIES          = 6
	VOYAGE_RETRY_BACKOFF        = 60 * time.Second // grows linearly per attempt (1min, 2min, 3min, ...)
)

// sleep is the seam every wait in this package goes through (the repo-wide
// function-variable DI convention). Pacing is behaviour worth asserting — "wait
// between calls but not after the last" is a real bug the eval would pay ~5
// minutes for — and asserting it by wall clock makes the test both slow and
// flaky. Tests swap this for a recorder.
var sleep = time.Sleep

// RateLimitError marks an HTTP 429 so the retry loop can back off and retry,
// while other errors fail fast.
type RateLimitError struct {
	Status string
	Body   string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited: %s: %s", e.Status, e.Body)
}

// estimateTokens approximates the token count of text. ~4 chars/token is the
// rule of thumb; the +1 and the safety pad on pacing absorb the error, and the
// 429 retry loop is the ultimate backstop if a batch still lands over budget.
func estimateTokens(text string) int {
	return len(text)/4 + 1
}

// planBatches groups consecutive texts into batches whose estimated token sum
// stays <= maxTokens and whose count stays <= maxCount, preserving order.
func planBatches(texts []string, maxTokens, maxCount int) [][]string {
	var batches [][]string
	var cur []string // batch being filled
	curTokens := 0
	for _, t := range texts {
		tk := estimateTokens(t)
		// Close the current batch when adding t would exceed either budget.
		// The len(cur) > 0 guard means a single text larger than maxTokens
		// still becomes its own (oversized) batch instead of an empty one —
		// the retry loop absorbs the risk of it tripping the rate limit.
		if len(cur) > 0 && (len(cur) >= maxCount || curTokens+tk > maxTokens) {
			batches = append(batches, cur)
			cur = nil
			curTokens = 0
		}
		cur = append(cur, t)
		curTokens += tk
	}
	if len(cur) > 0 {
		batches = append(batches, cur)
	}
	return batches
}

// pacingDelay returns how long to wait after sending a request costing
// requestTokens. A request of N tokens uses N/VOYAGE_TPM_LIMIT of the per-minute
// token budget, so sleeping that same fraction of 60s keeps the rolling token
// rate under the cap (padded by VOYAGE_RATE_SAFETY since N is only an estimate).
// The result is floored at VOYAGE_MIN_REQUEST_GAP, which independently keeps the
// request rate under the requests-per-minute cap.
//
// Both endpoints pace through this one function, because the throttle is the
// account's rather than the endpoint's. What differs between them is only the
// token count handed in: a batch of documents for embeddings, one query plus its
// documents for rerank (rerankTokens). At rag.RERANK_CANDIDATES_PER_STORY = 6
// chunks of ~800 words that is ~7.9K tokens, 79% of the minute's budget, so
// rerank returns ~52s and the TPM term - not the request gap - is what binds.
func pacingDelay(requestTokens int) time.Duration {
	d := time.Duration(float64(requestTokens) / float64(VOYAGE_TPM_LIMIT) * 60.0 * VOYAGE_RATE_SAFETY * float64(time.Second))
	if d < VOYAGE_MIN_REQUEST_GAP {
		d = VOYAGE_MIN_REQUEST_GAP
	}
	return d
}

// rerankTokens estimates what one rerank request costs against the token budget:
// the query plus every document, since the cross-encoder reads the pair. This is
// the number pacingDelay is computed from on the rerank path.
func rerankTokens(query string, documents []string) int {
	tokens := estimateTokens(query)
	for _, d := range documents {
		tokens += estimateTokens(d)
	}
	return tokens
}

// retryOn429 calls fn, retrying only on rate limits with linear backoff
// (backoff, 2*backoff, 3*backoff, ...). Rate limits are transient by definition
// - wait long enough and the budget refills - while any other error is returned
// immediately, since retrying wouldn't change the outcome.
//
// It takes a func() error rather than returning a value so both endpoints can
// share it: the caller closes over whatever its own call returns.
func retryOn429(fn func() error, maxRetries int, backoff time.Duration) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err

		var rle *RateLimitError
		if !errors.As(err, &rle) {
			return err // not a rate limit; fail fast
		}
		wait := backoff * time.Duration(attempt+1)
		log.Printf("voyage: rate limited (attempt %d/%d), backing off %s", attempt+1, maxRetries, wait)
		sleep(wait)
	}
	return fmt.Errorf("exhausted %d retries: %w", maxRetries, lastErr)
}

// embedBatchWithRetry calls embedBatch under the shared 429 retry policy (30s,
// 60s, 90s, ...).
func embedBatchWithRetry(texts []string, inputType string) ([][]float32, error) {
	var vecs [][]float32
	err := retryOn429(func() error {
		var err error
		vecs, err = embedBatch(texts, inputType)
		return err
	}, VOYAGE_MAX_RETRIES, VOYAGE_RETRY_BACKOFF)
	if err != nil {
		return nil, err
	}
	return vecs, nil
}
