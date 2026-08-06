package evalHarness

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// readPredictionsCSV parses a written predictions file back into records,
// header included, so assertions are on parsed columns rather than on raw text.
func readPredictionsCSV(t *testing.T, dir, runID string) [][]string {
	t.Helper()

	file, err := os.Open(filepath.Join(dir, runID+".csv"))
	if err != nil {
		t.Fatalf("predictions CSV not written under its run_id: %v", err)
	}
	defer file.Close()

	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatalf("predictions CSV is not valid CSV: %v", err)
	}
	return rows
}

func TestWritePredictionsCSVShape(t *testing.T) {
	dir := withStubbedRecording(t, time.Now(), "sha")

	dataset := []LabelledStory{
		{StoryID: 10, Title: "flagged and relevant", Label: 1},
		{StoryID: 20, Title: "flagged but a dud", Label: 0},
		{StoryID: 30, Title: "skipped and irrelevant", Label: 0},
		{StoryID: 40, Title: "skipped but relevant", Label: 1},
	}
	if err := writePredictionsCSV("run-1", []int{10, 20}, dataset); err != nil {
		t.Fatalf("writePredictionsCSV returned error: %v", err)
	}

	rows := readPredictionsCSV(t, dir, "run-1")

	if got, want := len(rows), len(dataset)+1; got != want {
		t.Fatalf("got %d rows, want %d (one per dataset item + header)", got, want)
	}

	for i, want := range PREDICTIONS_CSV_HEADER {
		if rows[0][i] != want {
			t.Errorf("header column %d = %q, want %q", i, rows[0][i], want)
		}
	}

	// Rows follow dataset order, and cover all four confusion-matrix cells.
	want := [][]string{
		{"run-1", "10", "flagged and relevant", "1", "1"},   // TP
		{"run-1", "20", "flagged but a dud", "0", "1"},      // FP
		{"run-1", "30", "skipped and irrelevant", "0", "0"}, // TN
		{"run-1", "40", "skipped but relevant", "1", "0"},   // FN
	}
	for i, wantRow := range want {
		for column, wantCell := range wantRow {
			if got := rows[i+1][column]; got != wantCell {
				t.Errorf("row %d column %q = %q, want %q", i, PREDICTIONS_CSV_HEADER[column], got, wantCell)
			}
		}
	}
}

// TestWritePredictionsCSVQuotesAwkwardTitles: HN titles routinely contain commas
// and quotes. If they were not escaped the columns would shift silently, and a
// shifted `predicted` column is worse than a missing file.
func TestWritePredictionsCSVQuotesAwkwardTitles(t *testing.T) {
	dir := withStubbedRecording(t, time.Now(), "sha")

	dataset := []LabelledStory{
		{StoryID: 1, Title: `Rust, Go, and "systems" programming`, Label: 1},
		{StoryID: 2, Title: "A title\nwith a newline", Label: 0},
	}
	if err := writePredictionsCSV("run-1", []int{1}, dataset); err != nil {
		t.Fatalf("writePredictionsCSV returned error: %v", err)
	}

	rows := readPredictionsCSV(t, dir, "run-1")
	if got, want := len(rows), 3; got != want {
		t.Fatalf("got %d rows, want %d - an unescaped title split a row", got, want)
	}
	for i, story := range dataset {
		if got := rows[i+1][2]; got != story.Title {
			t.Errorf("title round-tripped as %q, want %q", got, story.Title)
		}
	}
}

// TestPredictionsCSVAgreesWithMetrics is the point of sharing
// predictedPositiveSet: the per-story rows must add up to the confusion matrix
// they sit beside, or the CSV cannot be trusted as its breakdown.
func TestPredictionsCSVAgreesWithMetrics(t *testing.T) {
	dir := withStubbedRecording(t, time.Now(), "sha")

	dataset := []LabelledStory{
		{StoryID: 1, Label: 1}, {StoryID: 2, Label: 1}, {StoryID: 3, Label: 1},
		{StoryID: 4, Label: 1}, {StoryID: 5, Label: 0}, {StoryID: 6, Label: 0},
		{StoryID: 7, Label: 0}, {StoryID: 8, Label: 0},
	}
	predictedIDs := []int{1, 2, 3, 5, 6}

	metrics := Evaluate(predictedIDs, dataset)
	if err := writePredictionsCSV("run-1", predictedIDs, dataset); err != nil {
		t.Fatalf("writePredictionsCSV returned error: %v", err)
	}

	// Recompute the matrix from the CSV alone and compare to Evaluate's.
	var tp, fp, tn, fn int
	for _, row := range readPredictionsCSV(t, dir, "run-1")[1:] {
		label, err := strconv.Atoi(row[3])
		if err != nil {
			t.Fatalf("unparseable label %q: %v", row[3], err)
		}
		predicted, err := strconv.Atoi(row[4])
		if err != nil {
			t.Fatalf("unparseable prediction %q: %v", row[4], err)
		}
		switch {
		case predicted == 1 && label == 1:
			tp++
		case predicted == 1 && label == 0:
			fp++
		case predicted == 0 && label == 0:
			tn++
		default:
			fn++
		}
	}

	got := ConfusionMatrix{TP: tp, FP: fp, TN: tn, FN: fn}
	if got != metrics.Confusion {
		t.Errorf("matrix recomputed from CSV = %+v, want %+v", got, metrics.Confusion)
	}
}

// TestWritePredictionsCSVCreatesDirectory: same fresh-clone concern as the run
// record - evalRuns/ is git-ignored and so absent on a first run.
func TestWritePredictionsCSVCreatesDirectory(t *testing.T) {
	dir := withStubbedRecording(t, time.Now(), "sha")
	EVAL_RUNS_DIR = filepath.Join(dir, "does-not-exist-yet")

	if err := writePredictionsCSV("run-1", nil, []LabelledStory{{StoryID: 1}}); err != nil {
		t.Fatalf("writePredictionsCSV returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(EVAL_RUNS_DIR, "run-1.csv")); err != nil {
		t.Errorf("expected the predictions directory to be created: %v", err)
	}
}
