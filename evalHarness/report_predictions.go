package evalHarness

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jszwec/csvutil"
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
//
// The tags address columns BY NAME, which is not fussiness: `label` and
// `predicted` are both 0/1 values sitting next to each other, so a positional
// reader that got them the wrong way round would keep parsing happily and report
// confident numbers computed from the wrong field. Every other way this can go
// wrong is loud. Named columns also mean one the reader does not need - adding
// objectID is the case already discussed - leaves it working.
//
// Predicted is a bool over a column holding "1"/"0" because csvutil decodes
// bools with strconv.ParseBool, which accepts both.
type storyVerdict struct {
	StoryID   int  `csv:"story_id"`
	Label     int  `csv:"label"`
	Predicted bool `csv:"predicted"`
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

	// NewDecoder consumes the header, so an empty file fails here rather than
	// decoding as zero rows. Its EOF is restated: this error reaches the
	// operator as a warning explaining a missing stability block, and a bare
	// "EOF" does not say what to go and look at.
	decoder, err := csvutil.NewDecoder(csv.NewReader(file))
	if errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("predictions CSV %s is empty", path)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read predictions CSV %s: %w", path, err)
	}

	// ⚠️ Load-bearing. Without it a header missing `story_id` decodes every row
	// to the zero value, and 341 stories silently collapse into one - numbers
	// that look plausible and are wrong. Failing loudly is the whole reason this
	// reader validates anything at all.
	decoder.DisallowMissingColumns = true

	var verdicts []storyVerdict
	// io.EOF here means a header and no rows, which is an empty run rather than
	// a broken file.
	if err := decoder.Decode(&verdicts); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("predictions CSV %s: %w", path, err)
	}

	return verdicts, nil
}
