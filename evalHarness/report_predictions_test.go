package evalHarness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeCSVFile drops a literal CSV under dir, for the malformed cases that
// writePredictionsCSV could never produce.
func writeCSVFile(t *testing.T, dir, runID, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, runID+".csv"), []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test CSV: %v", err)
	}
}

// The reader's contract is with the writer, so the round trip is the test that
// matters: anything the two disagree about shows up here rather than in a
// hand-written fixture that agrees with neither.
func TestLoadPredictionsCSVRoundTripsTheWriter(t *testing.T) {
	dir := withStubbedRecording(t, time.Now(), "sha")

	dataset := []LabelledStory{
		{StoryID: 10, Title: "flagged and relevant", Label: 1},
		{StoryID: 20, Title: `flagged, with a "comma" and quotes`, Label: 0},
		{StoryID: 30, Title: "skipped and irrelevant", Label: 0},
		{StoryID: 40, Title: "skipped but relevant", Label: 1},
	}
	if err := writePredictionsCSV("run-1", []int{10, 20}, dataset); err != nil {
		t.Fatalf("writePredictionsCSV returned error: %v", err)
	}

	verdicts, err := loadPredictionsCSV(dir, "run-1")
	if err != nil {
		t.Fatalf("loadPredictionsCSV returned error: %v", err)
	}

	want := []storyVerdict{
		{StoryID: 10, Label: 1, Predicted: true},
		{StoryID: 20, Label: 0, Predicted: true},
		{StoryID: 30, Label: 0, Predicted: false},
		{StoryID: 40, Label: 1, Predicted: false},
	}
	if len(verdicts) != len(want) {
		t.Fatalf("got %d verdicts, want %d", len(verdicts), len(want))
	}
	for i, w := range want {
		if verdicts[i] != w {
			t.Errorf("verdict %d = %+v, want %+v", i, verdicts[i], w)
		}
	}
}

// Duplicate story IDs are returned as written. Collapsing them is the bucketing
// stage's job; doing it here would hide the dataset property behind it.
func TestLoadPredictionsCSVKeepsDuplicateStoryIDs(t *testing.T) {
	dir := t.TempDir()
	writeCSVFile(t, dir, "run-1", strings.Join([]string{
		"run_id,story_id,title,label,predicted",
		"run-1,10,a title,0,1",
		"run-1,10,the same story again,0,1",
		"",
	}, "\n"))

	verdicts, err := loadPredictionsCSV(dir, "run-1")
	if err != nil {
		t.Fatalf("loadPredictionsCSV returned error: %v", err)
	}
	if len(verdicts) != 2 {
		t.Fatalf("got %d verdicts, want 2 - a duplicate story_id was dropped at load time", len(verdicts))
	}
}

// Columns are found by name, so a new column the reader does not need must not
// disturb it. objectID is the concrete case already discussed on issue #37.
func TestLoadPredictionsCSVToleratesAnExtraColumn(t *testing.T) {
	dir := t.TempDir()
	writeCSVFile(t, dir, "run-1", strings.Join([]string{
		"run_id,objectID,story_id,title,label,predicted",
		"run-1,obj-1,10,a title,1,1",
		"",
	}, "\n"))

	verdicts, err := loadPredictionsCSV(dir, "run-1")
	if err != nil {
		t.Fatalf("loadPredictionsCSV returned error: %v", err)
	}
	want := storyVerdict{StoryID: 10, Label: 1, Predicted: true}
	if len(verdicts) != 1 || verdicts[0] != want {
		t.Errorf("got %+v, want exactly one %+v - columns were read positionally", verdicts, want)
	}
}

func TestLoadPredictionsCSVAddressesFilesByRunID(t *testing.T) {
	dir := t.TempDir()
	writeCSVFile(t, dir, "wanted-run", "run_id,story_id,title,label,predicted\nwanted-run,10,a title,1,1\n")
	writeCSVFile(t, dir, "other-run", "run_id,story_id,title,label,predicted\nother-run,99,another title,0,1\n")

	verdicts, err := loadPredictionsCSV(dir, "wanted-run")
	if err != nil {
		t.Fatalf("loadPredictionsCSV returned error: %v", err)
	}
	if len(verdicts) != 1 || verdicts[0].StoryID != 10 {
		t.Errorf("got %+v, want only wanted-run's row - the directory was scanned", verdicts)
	}
}

func TestLoadPredictionsCSVRejectsUnusableFiles(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantMessage string
	}{
		{
			name:        "missing required column",
			content:     "run_id,title,label,predicted\nrun-1,a title,1,1\n",
			wantMessage: `missing required column "story_id"`,
		},
		{
			name:        "story_id is not an integer",
			content:     "run_id,story_id,title,label,predicted\nrun-1,not-a-number,a title,1,1\n",
			wantMessage: `column "story_id" is not an integer`,
		},
		{
			name:        "predicted is not an integer",
			content:     "run_id,story_id,title,label,predicted\nrun-1,10,a title,1,yes\n",
			wantMessage: `column "predicted" is not an integer`,
		},
		{
			name:        "empty file",
			content:     "",
			wantMessage: "is empty",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			writeCSVFile(t, dir, "run-1", test.content)

			_, err := loadPredictionsCSV(dir, "run-1")
			if err == nil {
				t.Fatalf("loadPredictionsCSV accepted an unusable file")
			}
			if !strings.Contains(err.Error(), test.wantMessage) {
				t.Errorf("error = %q, want it to mention %q", err, test.wantMessage)
			}
		})
	}
}

// The error must name the file. A stability block is skipped on this failure,
// and "some CSV was unreadable" is not enough to go and fix it.
func TestLoadPredictionsCSVMissingFileNamesThePath(t *testing.T) {
	dir := t.TempDir()

	_, err := loadPredictionsCSV(dir, "never-written")
	if err == nil {
		t.Fatalf("loadPredictionsCSV accepted a missing file")
	}
	if !strings.Contains(err.Error(), "never-written.csv") {
		t.Errorf("error = %q, want it to name the missing file", err)
	}
}
