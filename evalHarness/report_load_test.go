package evalHarness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeRecordFile drops a record on disk under its own run ID, the way an eval
// run would.
func writeRecordFile(t *testing.T, dir string, record RunRecord) {
	t.Helper()

	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal fixture record: %v", err)
	}
	path := filepath.Join(dir, record.RunID+".json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write fixture record %s: %v", path, err)
	}
}

func testRecord(runID string, arm int) RunRecord {
	return RunRecord{
		RunID:           runID,
		Arm:             arm,
		GitSHA:          "1b4a0bec0ffee0ddba11decafbad00d15ea5e000",
		DatasetHash:     "a59941fa34f7000000000000000000000000000000000000000000000000aaaa",
		CorpusIndexHash: "3daf2eee4e65000000000000000000000000000000000000000000000000bbbb",
		Model:           "claude-haiku-4-5-20251001",
		Metrics:         RunMetrics{TP: 24, FP: 79, TN: 227, FN: 11, Precision: 0.2330, Recall: 0.6857},
	}
}

func TestLoadRunRecordsSortsByRunID(t *testing.T) {
	dir := t.TempDir()

	// Written out of order: the loader must sort, not rely on the glob's order.
	writeRecordFile(t, dir, testRecord("20260806T090351Z-arm-0", 0))
	writeRecordFile(t, dir, testRecord("20260806T084423Z-arm-2", 2))
	writeRecordFile(t, dir, testRecord("20260806T085514Z-arm-2", 2))

	records, skipped := loadRunRecords(dir)
	if len(skipped) != 0 {
		t.Fatalf("loadRunRecords returned %d skip errors, want 0: %v", len(skipped), skipped)
	}

	want := []string{"20260806T084423Z-arm-2", "20260806T085514Z-arm-2", "20260806T090351Z-arm-0"}
	if len(records) != len(want) {
		t.Fatalf("loaded %d records, want %d", len(records), len(want))
	}
	for i, id := range want {
		if records[i].RunID != id {
			t.Errorf("record %d = %q, want %q", i, records[i].RunID, id)
		}
	}
}

func TestLoadRunRecordsPreservesFields(t *testing.T) {
	dir := t.TempDir()
	writeRecordFile(t, dir, testRecord("20260806T084423Z-arm-2", 2))

	records, _ := loadRunRecords(dir)
	if len(records) != 1 {
		t.Fatalf("loaded %d records, want 1", len(records))
	}

	got := records[0]
	if got.Arm != 2 || got.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("arm/model round-tripped wrongly: arm=%d model=%q", got.Arm, got.Model)
	}
	if got.Metrics.TP != 24 || got.Metrics.FP != 79 || got.Metrics.TN != 227 || got.Metrics.FN != 11 {
		t.Errorf("confusion matrix round-tripped wrongly: %+v", got.Metrics)
	}
	if got.Metrics.Precision != 0.2330 || got.Metrics.Recall != 0.6857 {
		t.Errorf("rates round-tripped wrongly: %+v", got.Metrics)
	}
}

func TestLoadRunRecordsSkipsMalformedFileButKeepsTheRest(t *testing.T) {
	dir := t.TempDir()
	writeRecordFile(t, dir, testRecord("20260806T084423Z-arm-2", 2))
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0644); err != nil {
		t.Fatalf("failed to write broken fixture: %v", err)
	}

	records, skipped := loadRunRecords(dir)
	if len(records) != 1 || records[0].RunID != "20260806T084423Z-arm-2" {
		t.Fatalf("good record was lost alongside the broken one: %+v", records)
	}
	if len(skipped) != 1 {
		t.Fatalf("got %d skip errors, want 1: %v", len(skipped), skipped)
	}
}

// A JSON object that isn't a run record decodes cleanly into a zero RunRecord -
// encoding/json ignores unknown fields. Without the run_id check that becomes a
// fabricated arm-0 row scoring 0/0/0/0, which is worse than a skipped file.
func TestLoadRunRecordsSkipsJSONWithoutRunID(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notarecord.json"), []byte(`{"hello":"world"}`), 0644); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	records, skipped := loadRunRecords(dir)
	if len(records) != 0 {
		t.Fatalf("loaded %d records from a non-record file, want 0: %+v", len(records), records)
	}
	if len(skipped) != 1 {
		t.Fatalf("got %d skip errors, want 1: %v", len(skipped), skipped)
	}
}

// The archived dataset and corpus-index snapshots live in subdirectories and are
// also named <sha256>.json. A recursive walk would try to parse a 4.4 MB index as
// a run record; this pins that the glob stays non-recursive.
func TestLoadRunRecordsIgnoresArchivedArtifacts(t *testing.T) {
	dir := t.TempDir()
	writeRecordFile(t, dir, testRecord("20260806T084423Z-arm-2", 2))

	archiveDir := filepath.Join(dir, DATASETS_SUBDIR)
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		t.Fatalf("failed to create archive dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archiveDir, "deadbeef.json"), []byte(`[{"story_id":1}]`), 0644); err != nil {
		t.Fatalf("failed to write archived fixture: %v", err)
	}

	records, skipped := loadRunRecords(dir)
	if len(records) != 1 {
		t.Fatalf("loaded %d records, want 1 (archives must not be read)", len(records))
	}
	if len(skipped) != 0 {
		t.Fatalf("archived artifacts produced %d warnings, want 0: %v", len(skipped), skipped)
	}
}

// Zero runs is a true state, not a failure: before the first eval there is
// nothing to aggregate, and a missing directory says exactly that.
func TestLoadRunRecordsOnEmptyOrMissingDir(t *testing.T) {
	empty := t.TempDir()
	records, skipped := loadRunRecords(empty)
	if len(records) != 0 || len(skipped) != 0 {
		t.Errorf("empty dir: got %d records / %d errors, want 0/0", len(records), len(skipped))
	}

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	records, skipped = loadRunRecords(missing)
	if len(records) != 0 || len(skipped) != 0 {
		t.Errorf("missing dir: got %d records / %d errors, want 0/0", len(records), len(skipped))
	}
}
