package evalHarness

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/shrirambalakrishnan/tech-news/armcontext"
	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
	"github.com/shrirambalakrishnan/tech-news/rag"
)

// LABELLED_DATA_FILE is the hand-labelled dataset (git-ignored). Path is
// relative to the repo root, where `go run . eval` executes.
const LABELLED_DATA_FILE = "evalHarness/hn-responses-labelled.json"

// EVAL_BATCH_SIZE is how many stories we send to Claude per classify call. We
// match production's per-call size (~30, one front page) so the eval measures
// the same prompt shape we actually ship, and so the returned JSON ID array
// stays well under claudeapi's MaxTokens cap. See CLAUDE.md -> Eval harness.
// It's a mutable package var so tests can shrink it.
var EVAL_BATCH_SIZE = 30

// DI seams (same function-variable convention as the rest of the repo): tests
// swap these for fakes so the runner never touches the network or the disk.
// buildProfileForArm is the SAME function production calls - the eval is only
// transferable if both feed the classifier identical context.
var classifyTechNewsStory = hackernews_classifier.ClassifyTechNewsStory
var buildProfileForArm = armcontext.BuildProfile

// RunEval is the `go run . eval <arm>` entrypoint: load the labelled data, run
// the classifier over it in batches exactly as production would under the given
// arm, score the predictions against the human labels, print the report to the
// console, and record what the run used. It runs exactly one arm.
//
// It returns an error rather than calling log.Fatal so main keeps exactly one
// exit point - which matters here because the report and the exit code are
// decided separately: see the recording step below.
func RunEval(arm hackernews_classifier.Arm) error {
	runID := newRunID(arm)

	dataset, err := loadLabelledData(LABELLED_DATA_FILE)
	if err != nil {
		return fmt.Errorf("eval: failed to load labelled data: %w", err)
	}
	log.Printf("eval: loaded %d labelled stories from %s", len(dataset), LABELLED_DATA_FILE)

	// Snapshot the inputs before spending anything: free, local, and it pins the
	// exact bytes the run is about to read. A failure here aborts rather than
	// producing a number nobody can trace back to its data.
	artifacts, err := archiveRunInputs(arm, LABELLED_DATA_FILE, rag.CORPUS_INDEX_FILE)
	if err != nil {
		return fmt.Errorf("eval: %w", err)
	}

	// Everything above this line is free. Everything below spends money.
	predictedIDs, err := classifyInBatches(arm, dataset)
	if err != nil {
		return fmt.Errorf("eval: %w", err)
	}

	metrics := Evaluate(predictedIDs, dataset)

	// Console-log the full report (every metric + the FP/FN title lists) BEFORE
	// recording. An eval run costs real money and ~10 minutes, so a failure to
	// write the record must never cost the operator the report they paid for -
	// hence print first, then record, then return the recording error so the
	// exit code still says something went wrong.
	fmt.Print(metrics.Report())

	return recordRun(runID, arm, artifacts, metrics, predictedIDs, dataset)
}

// recordRun writes the run's descriptive artifacts. Separated from RunEval so
// the ordering rule above ("report first, then record") is visible at the call
// site rather than buried in a tail of writes.
//
// Both artifacts are named by the SAME runID, computed once at the top of
// RunEval rather than by each writer. Two independent timestamps taken seconds
// apart can straddle a second boundary and leave a .json and a .csv that no
// longer look like the same run.
//
// The CSV is written even when the record fails, and vice versa: they answer
// different questions (aggregate counts vs per-story verdicts), so one being
// unwritable is no reason to discard the other from a run already paid for.
// Errors are joined so neither failure hides the other.
//
// A classification failure never reaches here: there are no metrics to record,
// and a record of a run that produced no numbers would be misleading rather
// than incomplete.
func recordRun(runID string, arm hackernews_classifier.Arm, artifacts runArtifacts, metrics Metrics, predictedIDs []int, dataset []LabelledStory) error {
	var failures []error

	record, err := buildRunRecord(runID, arm, artifacts, metrics)
	if err != nil {
		failures = append(failures, fmt.Errorf("failed to build run record for %s: %w", runID, err))
	} else if err := writeRunRecord(record); err != nil {
		failures = append(failures, err)
	} else {
		log.Printf("eval: wrote run record %s", filepath.Join(EVAL_RUNS_DIR, runID+".json"))
	}

	if err := writePredictionsCSV(runID, predictedIDs, dataset); err != nil {
		failures = append(failures, err)
	} else {
		log.Printf("eval: wrote %d predictions to %s", len(dataset), filepath.Join(EVAL_RUNS_DIR, runID+".csv"))
	}

	if len(failures) > 0 {
		return fmt.Errorf("eval: %w", errors.Join(failures...))
	}
	return nil
}

// loadLabelledData reads and parses the JSON array of labelled stories.
func loadLabelledData(path string) ([]LabelledStory, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var dataset []LabelledStory
	if err := json.Unmarshal(raw, &dataset); err != nil {
		return nil, err
	}
	return dataset, nil
}

// classifyInBatches walks the dataset in EVAL_BATCH_SIZE chunks, classifies each
// chunk in one Claude call under the given arm, and concatenates the
// predicted-relevant IDs. This is the "batch 30" run shape: each call is the
// same size production sends.
//
// The profile is built per batch, from that batch's own stories, exactly as
// FilterHackerNewsStoriesByTitle does per run - so arm 2's retrieval is keyed by
// the batch's titles rather than reusing one global context, and the measured
// numbers stay transferable. Arms 0 and 1 rebuild an identical profile each time
// (a re-read of a small local JSON, next to a Claude call - not worth
// special-casing to keep one dispatch point).
func classifyInBatches(arm hackernews_classifier.Arm, dataset []LabelledStory) ([]int, error) {
	var predicted []int

	for start := 0; start < len(dataset); start += EVAL_BATCH_SIZE {
		end := start + EVAL_BATCH_SIZE
		if end > len(dataset) {
			end = len(dataset)
		}
		batch := dataset[start:end]

		// Map the labelled rows to the classifier's input contract (id + title only).
		stories := make([]hackernews_classifier.StoryDetail, 0, len(batch))
		for _, s := range batch {
			stories = append(stories, hackernews_classifier.StoryDetail{Id: s.StoryID, Title: s.Title})
		}

		// Errors abort the eval rather than falling back to a lesser arm: a run
		// labelled "RAG" must never be scored against the static ruleset (issue
		// #11). The first batch fails before any Claude call, so a missing
		// artifact costs nothing.
		batchProfile, err := buildProfileForArm(arm, stories)
		if err != nil {
			return nil, fmt.Errorf("batch %d-%d: %w", start, end-1, err)
		}

		ids := classifyTechNewsStory(arm, stories, batchProfile)
		predicted = append(predicted, ids...)

		log.Printf("eval: classified batch %d-%d (%d stories) -> %d flagged relevant",
			start, end-1, len(batch), len(ids))
	}

	return predicted, nil
}
