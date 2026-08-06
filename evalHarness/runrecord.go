package evalHarness

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shrirambalakrishnan/tech-news/claudeapi"
	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
)

// This file records WHAT an eval run used, so a number in the README can be
// traced back to the code, model and data that produced it.
//
// The eval harness answers "how good is the classifier". It does not answer
// "good under what" - which arm, which model, which commit's tuning constants.
// Today that context lives in whoever ran it, and it is gone by the next run.
// Every run now leaves a JSON record behind (issue #26).
//
// The record is DESCRIPTIVE, never an input: nothing reads it back to change
// how a run behaves. That is what keeps it safe to write after the run has
// already been scored.

// EVAL_RUNS_DIR is where run records are written, relative to the repo root
// (where `go run . eval` executes). Git-ignored: these are local run history,
// regenerable only by spending money again, so they are neither source of truth
// nor something to review in a diff. A mutable package var so tests can point it
// at t.TempDir().
var EVAL_RUNS_DIR = "evalRuns"

// RUN_ID_TIME_FORMAT stamps the run's UTC start time into its ID. Basic RFC3339
// (no colons) because the ID is also a filename: colons are illegal on Windows
// and awkward to quote in a shell. Lexical order is chronological order, so the
// folder sorts by time for free.
const RUN_ID_TIME_FORMAT = "20060102T150405Z"

// DI seams (the repo-wide function-variable convention): tests swap these so a
// record can be built without shelling out to git or depending on wall time.
var (
	now    = time.Now
	gitSHA = headCommitSHA
)

// RunMetrics is the metrics block of a run record: the confusion matrix plus the
// two rates. It deliberately mirrors evalHarness.Metrics MINUS the false
// positive/negative title lists - those are per-story detail, and per-story
// detail belongs in the run's CSV (one row per dataset item), not duplicated
// here in a shape that only holds the mistakes.
type RunMetrics struct {
	TP        int     `json:"tp"`
	FP        int     `json:"fp"`
	TN        int     `json:"tn"`
	FN        int     `json:"fn"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
}

// RunRecord is the artifact written to evalRuns/<run_id>.json after every eval.
//
// The point of each field is to pin one thing that can change the numbers:
//
//   - Arm      - which classification flow ran.
//   - GitSHA   - the commit, and so every tuning constant that lives in code
//     (RETRIEVAL_TOP_K, RETRIEVAL_POOL_CAP, the similarity floor, the prompt
//     text). This is why a tuning change must be committed to be recorded -
//     see headCommitSHA for the limitation that leaves.
//   - Model    - the LLM that classified. Changing it re-bases every number.
type RunRecord struct {
	RunID   string     `json:"run_id"`
	Arm     int        `json:"arm"`
	GitSHA  string     `json:"git_sha"`
	Model   string     `json:"model"`
	Metrics RunMetrics `json:"metrics"`
}

// newRunID builds the run's identity: "<UTC timestamp>-arm-<N>". It is both the
// record's ID and the stem of its filename, so a file can be traced to a run and
// back without opening it.
//
// The .UTC() belongs here rather than in the `now` seam: the ID's timezone is a
// property of the ID, not of whichever clock happens to be installed. Putting it
// in the seam would mean a run in a non-UTC zone stamps local time and IDs from
// two machines sort against each other wrongly.
func newRunID(arm hackernews_classifier.Arm) string {
	return fmt.Sprintf("%s-arm-%d", now().UTC().Format(RUN_ID_TIME_FORMAT), int(arm))
}

// headCommitSHA returns the full SHA of HEAD.
//
// ⚠️ It reports the last COMMIT, not the working tree. An uncommitted edit to a
// tuning constant is invisible here, so a record can claim a commit whose code
// is not what ran. Accepted deliberately (issue #26) to keep the record to its
// specified fields; the discipline it implies is to commit tuning changes before
// measuring them.
func headCommitSHA() (string, error) {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("failed to read git HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// buildRunRecord assembles the record from the run's identity and its scored
// metrics. Pure apart from the gitSHA seam, so the mapping is unit-testable.
func buildRunRecord(runID string, arm hackernews_classifier.Arm, metrics Metrics) (RunRecord, error) {
	sha, err := gitSHA()
	if err != nil {
		return RunRecord{}, err
	}

	return RunRecord{
		RunID:  runID,
		Arm:    int(arm),
		GitSHA: sha,
		Model:  claudeapi.ANTHROPIC_MODEL_NAME,
		Metrics: RunMetrics{
			TP:        metrics.Confusion.TP,
			FP:        metrics.Confusion.FP,
			TN:        metrics.Confusion.TN,
			FN:        metrics.Confusion.FN,
			Precision: metrics.Precision,
			Recall:    metrics.Recall,
		},
	}, nil
}

// writeRunRecord persists the record to evalRuns/<run_id>.json, creating the
// directory if needed.
func writeRunRecord(record RunRecord) error {
	if err := os.MkdirAll(EVAL_RUNS_DIR, 0755); err != nil {
		return fmt.Errorf("failed to create %s: %w", EVAL_RUNS_DIR, err)
	}

	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal run record: %w", err)
	}

	path := filepath.Join(EVAL_RUNS_DIR, record.RunID+".json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write run record to %s: %w", path, err)
	}
	return nil
}
