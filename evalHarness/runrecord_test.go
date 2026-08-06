package evalHarness

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shrirambalakrishnan/tech-news/claudeapi"
	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
)

// withStubbedRecording points EVAL_RUNS_DIR at a temp dir and pins the clock and
// the git SHA, so record tests are deterministic and never shell out. Returns
// the temp dir.
func withStubbedRecording(t *testing.T, at time.Time, sha string) string {
	t.Helper()

	dir := t.TempDir()
	originalDir, originalNow, originalSHA := EVAL_RUNS_DIR, now, gitSHA

	EVAL_RUNS_DIR = dir
	now = func() time.Time { return at }
	gitSHA = func() (string, error) { return sha, nil }

	t.Cleanup(func() {
		EVAL_RUNS_DIR, now, gitSHA = originalDir, originalNow, originalSHA
	})
	return dir
}

func TestNewRunIDFormat(t *testing.T) {
	withStubbedRecording(t, time.Date(2026, 8, 6, 9, 15, 0, 0, time.UTC), "abc123")

	// The ID is also a filename, so the assertion is on the exact string: no
	// colons (illegal on Windows), and lexically sortable by time.
	if got, want := newRunID(hackernews_classifier.ArmRAG), "20260806T091500Z-arm-2"; got != want {
		t.Errorf("newRunID = %q, want %q", got, want)
	}
}

// TestNewRunIDUsesUTC pins the "ensure it is UTC" requirement: a wall clock in
// another zone must still stamp the UTC instant, or IDs from two machines sort
// against each other wrongly.
func TestNewRunIDUsesUTC(t *testing.T) {
	// 2026-08-06 09:15:00 UTC expressed as 14:45 in a +05:30 zone.
	istZone := time.FixedZone("IST", int((5*time.Hour + 30*time.Minute).Seconds()))
	withStubbedRecording(t, time.Date(2026, 8, 6, 14, 45, 0, 0, istZone), "abc123")

	if got, want := newRunID(hackernews_classifier.ArmGeneric), "20260806T091500Z-arm-0"; got != want {
		t.Errorf("newRunID = %q, want %q (local time must be converted to UTC)", got, want)
	}
}

func TestBuildRunRecordMapsMetrics(t *testing.T) {
	withStubbedRecording(t, time.Now(), "fc057f7deadbeef")

	metrics := Metrics{
		Confusion: ConfusionMatrix{TP: 22, FP: 80, TN: 226, FN: 13},
		Precision: 0.2157,
		Recall:    0.6286,
		// The title lists must NOT leak into the record - they are the CSV's job.
		FalsePositives: []LabelledStory{{StoryID: 1, Title: "dud"}},
		FalseNegatives: []LabelledStory{{StoryID: 2, Title: "missed"}},
	}

	record, err := buildRunRecord("20260806T091500Z-arm-2", hackernews_classifier.ArmRAG,
		runArtifacts{DatasetHash: "dataset-sha", CorpusIndexHash: "index-sha"}, metrics)
	if err != nil {
		t.Fatalf("buildRunRecord returned error: %v", err)
	}

	if record.RunID != "20260806T091500Z-arm-2" {
		t.Errorf("RunID = %q", record.RunID)
	}
	if record.Arm != 2 {
		t.Errorf("Arm = %d, want 2", record.Arm)
	}
	if record.GitSHA != "fc057f7deadbeef" {
		t.Errorf("GitSHA = %q", record.GitSHA)
	}
	if record.Model != claudeapi.ANTHROPIC_MODEL_NAME {
		t.Errorf("Model = %q, want %q", record.Model, claudeapi.ANTHROPIC_MODEL_NAME)
	}
	if record.DatasetHash != "dataset-sha" {
		t.Errorf("DatasetHash = %q, want %q", record.DatasetHash, "dataset-sha")
	}
	if record.CorpusIndexHash != "index-sha" {
		t.Errorf("CorpusIndexHash = %q, want %q", record.CorpusIndexHash, "index-sha")
	}

	want := RunMetrics{TP: 22, FP: 80, TN: 226, FN: 13, Precision: 0.2157, Recall: 0.6286}
	if record.Metrics != want {
		t.Errorf("Metrics = %+v, want %+v", record.Metrics, want)
	}
}

// TestBuildRunRecordPropagatesGitFailure: without a SHA the record cannot pin
// the code that ran, so it must fail rather than record an empty string that
// silently reads as "no tuning to see here".
func TestBuildRunRecordPropagatesGitFailure(t *testing.T) {
	withStubbedRecording(t, time.Now(), "")
	gitSHA = func() (string, error) { return "", errors.New("not a git repository") }

	if _, err := buildRunRecord("run-1", hackernews_classifier.ArmGeneric, runArtifacts{}, Metrics{}); err == nil {
		t.Fatal("expected an error when the git SHA is unavailable, got nil")
	}
}

func TestWriteRunRecordRoundTrip(t *testing.T) {
	dir := withStubbedRecording(t, time.Now(), "sha")

	want := RunRecord{
		RunID:   "20260806T091500Z-arm-1",
		Arm:     1,
		GitSHA:  "sha",
		Model:   "claude-haiku-4-5-20251001",
		Metrics: RunMetrics{TP: 4, FP: 8, TN: 298, FN: 31, Precision: 0.3333, Recall: 0.1143},
	}
	if err := writeRunRecord(want); err != nil {
		t.Fatalf("writeRunRecord returned error: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "20260806T091500Z-arm-1.json"))
	if err != nil {
		t.Fatalf("record not written under its run_id: %v", err)
	}

	var got RunRecord
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("record is not valid JSON: %v", err)
	}
	if got != want {
		t.Errorf("round-tripped record = %+v, want %+v", got, want)
	}
}

// TestWriteRunRecordCreatesDirectory: the first eval on a fresh clone must not
// fail because evalRuns/ is git-ignored and therefore absent.
func TestWriteRunRecordCreatesDirectory(t *testing.T) {
	dir := withStubbedRecording(t, time.Now(), "sha")
	EVAL_RUNS_DIR = filepath.Join(dir, "does-not-exist-yet")

	if err := writeRunRecord(RunRecord{RunID: "run-1"}); err != nil {
		t.Fatalf("writeRunRecord returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(EVAL_RUNS_DIR, "run-1.json")); err != nil {
		t.Errorf("expected the record directory to be created: %v", err)
	}
}

// TestRunRecordCorpusIndexHashPresence pins the acceptance criterion that arms 0
// and 1 record NO corpus_index_hash. The distinction matters when reading a
// record back: an absent field says "this arm does not use the index", whereas
// an empty string would say "it used an index with no content".
func TestRunRecordCorpusIndexHashPresence(t *testing.T) {
	withStubbedRecording(t, time.Now(), "sha")

	tests := []struct {
		name      string
		arm       hackernews_classifier.Arm
		artifacts runArtifacts
		wantField bool
	}{
		{"arm 0 does not read the index", hackernews_classifier.ArmGeneric,
			runArtifacts{DatasetHash: "d"}, false},
		{"arm 1 does not read the index", hackernews_classifier.ArmInterests,
			runArtifacts{DatasetHash: "d"}, false},
		{"arm 2 reads the index", hackernews_classifier.ArmRAG,
			runArtifacts{DatasetHash: "d", CorpusIndexHash: "i"}, true},
		{"arm 3 reads the index", hackernews_classifier.ArmRAGPerStory,
			runArtifacts{DatasetHash: "d", CorpusIndexHash: "i"}, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record, err := buildRunRecord("run-1", test.arm, test.artifacts, Metrics{})
			if err != nil {
				t.Fatalf("buildRunRecord returned error: %v", err)
			}
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}

			var fields map[string]any
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}

			if _, present := fields["corpus_index_hash"]; present != test.wantField {
				t.Errorf("corpus_index_hash present = %v, want %v", present, test.wantField)
			}
			// dataset_hash is never omitted - every arm reads the dataset.
			if _, present := fields["dataset_hash"]; !present {
				t.Error("dataset_hash must be present for every arm")
			}
		})
	}
}
