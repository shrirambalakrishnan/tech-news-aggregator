package evalHarness

import (
	"encoding/csv"
	"errors"
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

	columns := map[string]int{}
	for i, name := range rows[0] {
		columns[name] = i
	}

	verdicts := make([]storyVerdict, 0, len(rows)-1)
	for i, row := range rows[1:] {
		storyID, idErr := intColumn(row, columns, "story_id")
		label, labelErr := intColumn(row, columns, "label")
		predicted, predictedErr := intColumn(row, columns, "predicted")

		if err := errors.Join(idErr, labelErr, predictedErr); err != nil {
			// +2: past the header, and back to 1-based line numbers.
			return nil, fmt.Errorf("predictions CSV %s line %d: %w", path, i+2, err)
		}
		verdicts = append(verdicts, storyVerdict{StoryID: storyID, Label: label, Predicted: predicted == 1})
	}

	return verdicts, nil
}

// intColumn reads one named column as an integer, erroring by name when the
// column is absent.
//
// BY NAME rather than by position, which is not fussiness: `label` and
// `predicted` are both 0/1 ints sitting next to each other, so a positional
// reader that got them the wrong way round would keep parsing happily and report
// confident numbers computed from the wrong field. Every other way this can go
// wrong is loud. It also means a column the reader does not need - adding
// objectID is the case already discussed - leaves it working.
//
// Indexing is safe without a length check: csv.Reader takes FieldsPerRecord from
// the header and rejects any row that doesn't match, so every row is exactly as
// long as the header the indexes came from.
func intColumn(row []string, columns map[string]int, name string) (int, error) {
	index, found := columns[name]
	if !found {
		return 0, fmt.Errorf("missing column %q", name)
	}

	value, err := strconv.Atoi(row[index])
	if err != nil {
		return 0, fmt.Errorf("column %q is not an integer: %w", name, err)
	}
	return value, nil
}
