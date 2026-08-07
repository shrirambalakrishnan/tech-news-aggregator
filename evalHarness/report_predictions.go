package evalHarness

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// This file is the second INPUT stage of the eval-run aggregator (issue #37):
// report_load.go reads the run records, this reads the per-story predictions
// CSVs those records sit beside.
//
// The record carries the four confusion-matrix counts, and counts are anonymous
// - "tp": 24 is 24 tally marks, not 24 named stories. So Output 2 can say a
// configuration averaged 22.5 TP across two runs, but not whether it was the
// same stories both times. That distinction decides what work the misses
// deserve: a story no run ever catches needs retrieval, prompt or corpus work,
// while one the model catches only sometimes needs a sampling fix. Identity was
// never stored in the record, so no amount of arithmetic over the counts
// recovers it - it has to be read back from the CSV.
//
// Reading is DESCRIPTIVE, exactly as report_load.go is: nothing here changes how
// a run behaves.

// storyVerdict is one CSV row reduced to the three columns the stability
// bucketing needs. The title is deliberately dropped - issue #37 prints counts
// only, and carrying titles would invite a "which stories" list the ticket
// explicitly parked.
type storyVerdict struct {
	StoryID   int
	Label     int
	Predicted bool
}

// loadPredictions is the DI seam (the repo-wide function-variable convention),
// so the rendering tests never touch disk - the same shape as loadRecords.
var loadPredictions = loadPredictionsCSV

// PREDICTIONS_REQUIRED_COLUMNS are the columns the stability bucketing reads.
// A subset of PREDICTIONS_CSV_HEADER, not the whole of it, and looked up BY NAME
// rather than by position: a later change adding a column (objectID is the one
// already discussed) would break a positional reader and a strict header-equality
// check, while this keeps reading the columns it actually needs.
var PREDICTIONS_REQUIRED_COLUMNS = []string{"story_id", "label", "predicted"}

// loadPredictionsCSV reads evalRuns/<run_id>.csv into one storyVerdict per row.
//
// Addressed BY RUN ID, never by scanning the directory: the group being reported
// on already names its members, and globbing could pick up a CSV belonging to a
// different configuration - which would silently mix two experiments into one
// stability table.
//
// Rows are returned as they appear, duplicates included. Collapsing duplicate
// story IDs is the bucketing stage's decision, and doing it here would hide the
// dataset property that makes the collapse necessary.
func loadPredictionsCSV(dir, runID string) ([]storyVerdict, error) {
	path := filepath.Join(dir, runID+".csv")

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open predictions CSV %s: %w", path, err)
	}
	defer file.Close()

	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("failed to read predictions CSV %s: %w", path, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("predictions CSV %s is empty", path)
	}

	columns, err := predictionColumnIndexes(rows[0])
	if err != nil {
		return nil, fmt.Errorf("predictions CSV %s: %w", path, err)
	}

	verdicts := make([]storyVerdict, 0, len(rows)-1)
	for i, row := range rows[1:] {
		verdict, err := parseVerdict(row, columns)
		if err != nil {
			// +2: past the header, and back to 1-based line numbers.
			return nil, fmt.Errorf("predictions CSV %s line %d: %w", path, i+2, err)
		}
		verdicts = append(verdicts, verdict)
	}

	return verdicts, nil
}

// predictionColumnIndexes maps each required column name to its position in the
// header, erroring by name when one is absent. Naming the missing column matters
// more than it looks: the failure mode this replaces is reading the wrong column
// silently and reporting confident numbers computed from the wrong field.
func predictionColumnIndexes(header []string) (map[string]int, error) {
	positions := map[string]int{}
	for i, name := range header {
		positions[name] = i
	}

	columns := map[string]int{}
	for _, name := range PREDICTIONS_REQUIRED_COLUMNS {
		index, found := positions[name]
		if !found {
			return nil, fmt.Errorf("missing required column %q", name)
		}
		columns[name] = index
	}
	return columns, nil
}

// parseVerdict reads one row through the resolved column positions.
func parseVerdict(row []string, columns map[string]int) (storyVerdict, error) {
	storyID, err := intColumn(row, columns, "story_id")
	if err != nil {
		return storyVerdict{}, err
	}
	label, err := intColumn(row, columns, "label")
	if err != nil {
		return storyVerdict{}, err
	}
	predicted, err := intColumn(row, columns, "predicted")
	if err != nil {
		return storyVerdict{}, err
	}

	return storyVerdict{StoryID: storyID, Label: label, Predicted: predicted == 1}, nil
}

// intColumn reads one named column as an integer. The length check guards short
// rows: encoding/csv rejects ragged rows by default, but a caller could disable
// that, and indexing past the end would panic rather than skip a file.
func intColumn(row []string, columns map[string]int, name string) (int, error) {
	index := columns[name]
	if index >= len(row) {
		return 0, fmt.Errorf("row has %d fields, too short for column %q", len(row), name)
	}

	value, err := strconv.Atoi(row[index])
	if err != nil {
		return 0, fmt.Errorf("column %q is not an integer: %w", name, err)
	}
	return value, nil
}
