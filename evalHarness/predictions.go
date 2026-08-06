package evalHarness

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// This file records the per-story verdicts behind a run's metrics.
//
// The run record (runrecord.go) carries the four confusion-matrix counts. Those
// counts say a run scored 22 TP and 80 FP; they cannot say WHICH stories. The
// console report lists the mistakes, but it is a transcript - it scrolls past
// and is not a dataset. Between two runs, "what changed" is a question about
// individual stories, and answering it needs both runs' verdicts side by side.
//
// So every run also writes a CSV with one row per dataset item. CSV rather than
// JSON because the natural next step is to diff or join two runs, which is what
// spreadsheets and `csvdiff` already do (calibrate-floor emits CSV for the same
// reason).

// PREDICTIONS_CSV_HEADER is the column order. story_id is the join key between
// two runs; label never changes for a given dataset, but is carried anyway so a
// single file is self-contained and can be read without the dataset beside it.
var PREDICTIONS_CSV_HEADER = []string{"run_id", "story_id", "title", "label", "predicted"}

// writePredictionsCSV writes evalRuns/<run_id>.csv: the header plus one row per
// dataset item, in dataset order.
//
// `predicted` is derived from the SAME set the confusion matrix is derived from
// (predictedPositiveSet), so the rows here always add up to the counts in the
// run record. That is a structural guarantee, not a convention: both callers go
// through one function.
func writePredictionsCSV(runID string, predictedPositiveIDs []int, dataset []LabelledStory) error {
	if err := os.MkdirAll(EVAL_RUNS_DIR, 0755); err != nil {
		return fmt.Errorf("failed to create %s: %w", EVAL_RUNS_DIR, err)
	}

	path := filepath.Join(EVAL_RUNS_DIR, runID+".csv")
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create predictions CSV %s: %w", path, err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	if err := writer.Write(PREDICTIONS_CSV_HEADER); err != nil {
		return fmt.Errorf("failed to write predictions CSV header: %w", err)
	}

	predicted := predictedPositiveSet(predictedPositiveIDs)
	for _, story := range dataset {
		// encoding/csv quotes titles containing commas or quotes - HN titles
		// routinely do, and a hand-rolled join would silently shift columns.
		row := []string{
			runID,
			strconv.Itoa(story.StoryID),
			story.Title,
			strconv.Itoa(story.Label),
			strconv.Itoa(boolToInt(predicted[story.StoryID])),
		}
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("failed to write predictions CSV row for story %d: %w", story.StoryID, err)
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("failed to flush predictions CSV %s: %w", path, err)
	}
	return nil
}

// boolToInt renders a verdict as 1/0 to match the dataset's own label encoding,
// so `label` and `predicted` are directly comparable in a spreadsheet.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
