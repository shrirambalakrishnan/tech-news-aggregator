package voyageapi

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

// captureLog redirects the standard logger for the duration of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// TestPaceAnnouncesTheWait is the whole reason pace exists. A paced run's normal
// behaviour is minutes of silence after a line describing a request that already
// succeeded, which is indistinguishable from a hung connection — the confusion
// that prompted this. The line has to name the delay and the two numbers it is
// computed from, since those are what an operator would change.
func TestPaceAnnouncesTheWait(t *testing.T) {
	gap, safety := VOYAGE_MIN_REQUEST_GAP, VOYAGE_RATE_SAFETY
	VOYAGE_MIN_REQUEST_GAP = time.Second
	VOYAGE_RATE_SAFETY = 0 // zero the token term so the floor governs
	t.Cleanup(func() { VOYAGE_MIN_REQUEST_GAP, VOYAGE_RATE_SAFETY = gap, safety })

	logged := captureLog(t)
	start := time.Now()
	pace("rerank request", 7859)
	elapsed := time.Since(start)

	line := logged.String()
	for _, want := range []string{"pacing 1s", "rerank request", "7859", "10000"} {
		if !strings.Contains(line, want) {
			t.Errorf("pacing log is missing %q: %s", want, line)
		}
	}
	// It must actually wait, not merely say so.
	if elapsed < VOYAGE_MIN_REQUEST_GAP {
		t.Errorf("pace returned after %s, want at least %s", elapsed, VOYAGE_MIN_REQUEST_GAP)
	}
}

// TestPaceIsSilentAndInstantWhenPacingIsOff: a zero delay is the configuration
// where pacing was switched off deliberately — tests, or an account with a
// payment method — and a line per request announcing a zero wait is noise.
func TestPaceIsSilentAndInstantWhenPacingIsOff(t *testing.T) {
	noPacing(t)

	logged := captureLog(t)
	start := time.Now()
	pace("embed batch", 7859)
	elapsed := time.Since(start)

	if logged.String() != "" {
		t.Errorf("expected no log line when the delay is zero, got: %s", logged.String())
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("pace slept %s with pacing switched off", elapsed)
	}
}
