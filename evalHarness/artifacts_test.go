package evalHarness

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
)

// writeTempFile creates a source artifact to be hashed/archived and returns its
// path.
func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	return path
}

// TestHashFileMatchesShasum pins the hash to plain sha256 over the raw bytes -
// the property that lets `shasum -a 256 <file>` reproduce it outside this
// program. Computed here independently rather than by calling hashFile twice.
func TestHashFileMatchesShasum(t *testing.T) {
	content := `{"schema_version":1,"chunks":[]}`
	path := writeTempFile(t, "index.json", content)

	got, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile returned error: %v", err)
	}

	sum := sha256.Sum256([]byte(content))
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Errorf("hashFile = %q, want %q", got, want)
	}
}

func TestHashFileDistinguishesContent(t *testing.T) {
	a, err := hashFile(writeTempFile(t, "a.json", `{"a":1}`))
	if err != nil {
		t.Fatalf("hashFile returned error: %v", err)
	}
	b, err := hashFile(writeTempFile(t, "b.json", `{"a":2}`))
	if err != nil {
		t.Fatalf("hashFile returned error: %v", err)
	}
	if a == b {
		t.Error("different content must hash differently")
	}
}

func TestHashFileErrorsOnMissingFile(t *testing.T) {
	if _, err := hashFile(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("expected an error for a missing file, got nil")
	}
}

func TestArchiveIfAbsentCopiesUnderItsHash(t *testing.T) {
	dir := withStubbedRecording(t, time.Now(), "sha")
	content := `{"chunks":[1,2,3]}`
	src := writeTempFile(t, "corpus_index.json", content)

	hash, err := archiveIfAbsent(CORPUS_INDEX_SUBDIR, src)
	if err != nil {
		t.Fatalf("archiveIfAbsent returned error: %v", err)
	}

	// The returned hash IS the filename - that is what makes a record's hash
	// enough to find the bytes.
	archived, err := os.ReadFile(filepath.Join(dir, CORPUS_INDEX_SUBDIR, hash+".json"))
	if err != nil {
		t.Fatalf("artifact not archived under its hash: %v", err)
	}
	if string(archived) != content {
		t.Errorf("archived content = %q, want %q", archived, content)
	}
}

// TestArchiveIfAbsentIsWriteIfAbsent is the acceptance criterion "re-running
// with an unchanged dataset and index adds NO new files". Asserted by counting
// directory entries, so an extra copy under any name fails.
func TestArchiveIfAbsentIsWriteIfAbsent(t *testing.T) {
	dir := withStubbedRecording(t, time.Now(), "sha")
	src := writeTempFile(t, "dataset.json", `[{"story_id":1}]`)

	first, err := archiveIfAbsent(DATASETS_SUBDIR, src)
	if err != nil {
		t.Fatalf("first archiveIfAbsent returned error: %v", err)
	}
	second, err := archiveIfAbsent(DATASETS_SUBDIR, src)
	if err != nil {
		t.Fatalf("second archiveIfAbsent returned error: %v", err)
	}

	if first != second {
		t.Errorf("same content hashed differently across calls: %q vs %q", first, second)
	}

	entries, err := os.ReadDir(filepath.Join(dir, DATASETS_SUBDIR))
	if err != nil {
		t.Fatalf("failed to read archive dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("got %d archived files after two runs over one artifact, want 1", len(entries))
	}
}

// TestArchiveIfAbsentStoresEachDistinctVersion: the flip side of write-if-absent.
// Changing the dataset (or re-embedding) must NOT silently overwrite the
// snapshot an older record still points at.
func TestArchiveIfAbsentStoresEachDistinctVersion(t *testing.T) {
	dir := withStubbedRecording(t, time.Now(), "sha")

	before := writeTempFile(t, "before.json", `{"chunks":[1]}`)
	after := writeTempFile(t, "after.json", `{"chunks":[1,2]}`)

	oldHash, err := archiveIfAbsent(CORPUS_INDEX_SUBDIR, before)
	if err != nil {
		t.Fatalf("archiveIfAbsent returned error: %v", err)
	}
	if _, err := archiveIfAbsent(CORPUS_INDEX_SUBDIR, after); err != nil {
		t.Fatalf("archiveIfAbsent returned error: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, CORPUS_INDEX_SUBDIR))
	if err != nil {
		t.Fatalf("failed to read archive dir: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("got %d archived files for two distinct indexes, want 2", len(entries))
	}

	// The older snapshot must still hold its original bytes.
	old, err := os.ReadFile(filepath.Join(dir, CORPUS_INDEX_SUBDIR, oldHash+".json"))
	if err != nil {
		t.Fatalf("older snapshot lost: %v", err)
	}
	if string(old) != `{"chunks":[1]}` {
		t.Errorf("older snapshot was overwritten: got %q", old)
	}
}

func TestArmUsesCorpusIndex(t *testing.T) {
	tests := []struct {
		arm  hackernews_classifier.Arm
		want bool
	}{
		{hackernews_classifier.ArmGeneric, false},
		{hackernews_classifier.ArmInterests, false},
		{hackernews_classifier.ArmRAG, true},
		{hackernews_classifier.ArmRAGPerStory, true},
	}
	for _, test := range tests {
		if got := armUsesCorpusIndex(test.arm); got != test.want {
			t.Errorf("armUsesCorpusIndex(arm %d) = %v, want %v", int(test.arm), got, test.want)
		}
	}
}

// TestArchiveRunInputsSkipsIndexForNonRetrievalArms: arms 0 and 1 must not even
// LOOK at the index. Enforced by pointing them at a path that does not exist -
// if they touched it, the call would error.
func TestArchiveRunInputsSkipsIndexForNonRetrievalArms(t *testing.T) {
	dir := withStubbedRecording(t, time.Now(), "sha")
	dataset := writeTempFile(t, "dataset.json", `[{"story_id":1}]`)
	absentIndex := filepath.Join(t.TempDir(), "no-such-index.json")

	for _, arm := range []hackernews_classifier.Arm{
		hackernews_classifier.ArmGeneric, hackernews_classifier.ArmInterests,
	} {
		artifacts, err := archiveRunInputs(arm, dataset, absentIndex)
		if err != nil {
			t.Fatalf("arm %d: archiveRunInputs returned error: %v", int(arm), err)
		}
		if artifacts.DatasetHash == "" {
			t.Errorf("arm %d: dataset must always be hashed", int(arm))
		}
		if artifacts.CorpusIndexHash != "" {
			t.Errorf("arm %d: must not hash the corpus index, got %q", int(arm), artifacts.CorpusIndexHash)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, CORPUS_INDEX_SUBDIR)); !os.IsNotExist(err) {
		t.Error("non-retrieval arms must not create the corpus_index archive dir")
	}
}

func TestArchiveRunInputsHashesBothForRetrievalArms(t *testing.T) {
	withStubbedRecording(t, time.Now(), "sha")
	dataset := writeTempFile(t, "dataset.json", `[{"story_id":1}]`)
	index := writeTempFile(t, "corpus_index.json", `{"chunks":[1]}`)

	for _, arm := range []hackernews_classifier.Arm{
		hackernews_classifier.ArmRAG, hackernews_classifier.ArmRAGPerStory,
	} {
		artifacts, err := archiveRunInputs(arm, dataset, index)
		if err != nil {
			t.Fatalf("arm %d: archiveRunInputs returned error: %v", int(arm), err)
		}
		if artifacts.DatasetHash == "" || artifacts.CorpusIndexHash == "" {
			t.Errorf("arm %d: want both hashes, got %+v", int(arm), artifacts)
		}
	}
}

// TestArchiveRunInputsFailsWhenIndexMissing: a retrieval arm with no index is
// aborted here, before any Claude call, rather than recorded with an unknown
// input. Same no-fail-soft rule as issue #11.
func TestArchiveRunInputsFailsWhenIndexMissing(t *testing.T) {
	withStubbedRecording(t, time.Now(), "sha")
	dataset := writeTempFile(t, "dataset.json", `[{"story_id":1}]`)
	absentIndex := filepath.Join(t.TempDir(), "no-such-index.json")

	if _, err := archiveRunInputs(hackernews_classifier.ArmRAG, dataset, absentIndex); err == nil {
		t.Fatal("expected an error when a retrieval arm has no corpus index, got nil")
	}
}

func TestArchiveRunInputsFailsWhenDatasetMissing(t *testing.T) {
	withStubbedRecording(t, time.Now(), "sha")
	absent := filepath.Join(t.TempDir(), "no-such-dataset.json")

	if _, err := archiveRunInputs(hackernews_classifier.ArmGeneric, absent, ""); err == nil {
		t.Fatal("expected an error when the dataset is missing, got nil")
	}
}
