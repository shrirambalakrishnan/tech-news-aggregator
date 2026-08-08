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
//  3. retry + backoff (embedBatchWithRetry) — the backstop when 1+2 still
//     land a 429, since token counts are only estimated
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

// pacingDelay returns how long to wait after sending a batch of batchTokens.
// A batch of N tokens uses N/VOYAGE_TPM_LIMIT of the per-minute token budget,
// so sleeping that same fraction of 60s keeps the rolling token rate under the
// cap (padded by VOYAGE_RATE_SAFETY since N is only an estimate). The result is
// floored at VOYAGE_MIN_REQUEST_GAP, which independently keeps the request
// rate under the requests-per-minute cap.
func pacingDelay(batchTokens int) time.Duration {
	d := time.Duration(float64(batchTokens) / float64(VOYAGE_TPM_LIMIT) * 60.0 * VOYAGE_RATE_SAFETY * float64(time.Second))
	if d < VOYAGE_MIN_REQUEST_GAP {
		d = VOYAGE_MIN_REQUEST_GAP
	}
	return d
}

// embedBatchWithRetry calls embedBatch, retrying on 429s with linear backoff
// (1min, 2min, 3min, ...). Only rate limits are retried — they are transient by
// definition (wait long enough and the budget refills); any other error is
// returned immediately since retrying wouldn't change the outcome.
func embedBatchWithRetry(texts []string, inputType string) ([][]float32, error) {
	var lastErr error
	for attempt := 0; attempt <= VOYAGE_MAX_RETRIES; attempt++ {
		vecs, err := embedBatch(texts, inputType)
		if err == nil {
			return vecs, nil
		}
		lastErr = err

		var rle *RateLimitError
		if !errors.As(err, &rle) {
			return nil, err // not a rate limit; fail fast
		}
		backoff := VOYAGE_RETRY_BACKOFF * time.Duration(attempt+1)
		log.Printf("voyage: rate limited (attempt %d/%d), backing off %s", attempt+1, VOYAGE_MAX_RETRIES, backoff)
		time.Sleep(backoff)
	}
	return nil, fmt.Errorf("exhausted %d retries: %w", VOYAGE_MAX_RETRIES, lastErr)
}
