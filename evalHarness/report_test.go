package evalHarness

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// withStubbedRecords swaps the loader seam so the rendering tests never touch
// disk, and restores it afterwards.
func withStubbedRecords(t *testing.T, records []RunRecord, skipped []error) {
	t.Helper()

	original := loadRecords
	loadRecords = func(string) ([]RunRecord, []error) { return records, skipped }
	t.Cleanup(func() { loadRecords = original })
}

// renderForTest runs the report and returns (stdout, stderr).
func renderForTest(t *testing.T) (string, string) {
	t.Helper()

	var out, warn bytes.Buffer
	if err := renderEvalReport(&out, &warn); err != nil {
		t.Fatalf("renderEvalReport returned error: %v", err)
	}
	return out.String(), warn.String()
}

func TestFormatRunsTableOneRowPerRunInOrder(t *testing.T) {
	records := []RunRecord{
		testRecord("20260806T084423Z-arm-2", 2),
		testRecord("20260806T085514Z-arm-2", 2),
		testRecord("20260806T090525Z-arm-3", 3),
	}

	table := formatRunsTable(records)
	lines := strings.Split(strings.TrimRight(table, "\n"), "\n")

	if len(lines) != len(records)+1 { // +1 for the header row
		t.Fatalf("table has %d lines, want %d:\n%s", len(lines), len(records)+1, table)
	}
	for i, r := range records {
		if !strings.HasPrefix(lines[i+1], r.RunID) {
			t.Errorf("row %d = %q, want it to start with %q", i, lines[i+1], r.RunID)
		}
	}
}

func TestFormatRunsTableShortensHashes(t *testing.T) {
	record := testRecord("20260806T084423Z-arm-2", 2)
	table := formatRunsTable([]RunRecord{record})

	if !strings.Contains(table, "1b4a0be") {
		t.Errorf("table missing the 7-char git sha:\n%s", table)
	}
	if strings.Contains(table, record.GitSHA) {
		t.Errorf("table printed the full git sha, want it abbreviated:\n%s", table)
	}
	if !strings.Contains(table, "a59941fa34f7") || !strings.Contains(table, "3daf2eee4e65") {
		t.Errorf("table missing the 12-char content hashes:\n%s", table)
	}
	// The model is a grouping key: truncating it could make two genuinely
	// different models look like one configuration.
	if !strings.Contains(table, record.Model) {
		t.Errorf("table must print the model in full:\n%s", table)
	}
}

// Arms 0 and 1 never load the corpus index, so their records carry no hash. That
// absence must not render as a blank cell or a zero, either of which reads as a
// value that was measured.
func TestFormatRunsTableMarksAbsentCorpusIndex(t *testing.T) {
	record := testRecord("20260806T090351Z-arm-0", 0)
	record.CorpusIndexHash = ""

	table := formatRunsTable([]RunRecord{record})
	if !strings.Contains(table, ABSENT_VALUE) {
		t.Errorf("expected %q for the absent corpus index hash:\n%s", ABSENT_VALUE, table)
	}
}

func TestRenderEvalReportPrintsRunCount(t *testing.T) {
	withStubbedRecords(t, []RunRecord{
		testRecord("20260806T084423Z-arm-2", 2),
		testRecord("20260806T085514Z-arm-2", 2),
	}, nil)

	out, warn := renderForTest(t)
	if !strings.Contains(out, "=== Eval runs (2) ===") {
		t.Errorf("missing run-count header:\n%s", out)
	}
	if warn != "" {
		t.Errorf("unexpected warnings: %q", warn)
	}
}

// A skipped record makes the history incomplete, so the count has to appear in
// the report itself - not only in a stderr line that scrolls past or is lost when
// stdout is redirected to a file.
func TestRenderEvalReportSurfacesSkippedRecords(t *testing.T) {
	withStubbedRecords(t,
		[]RunRecord{testRecord("20260806T084423Z-arm-2", 2)},
		[]error{errors.New("skipped evalRuns/broken.json: not a valid run record")},
	)

	out, warn := renderForTest(t)
	if !strings.Contains(out, "=== Eval runs (1, 1 skipped) ===") {
		t.Errorf("skip count missing from the table header:\n%s", out)
	}
	if !strings.Contains(warn, "broken.json") {
		t.Errorf("skip detail missing from the warning stream: %q", warn)
	}
}

// Zero runs is a true state (nothing has been evaluated yet), not an error.
func TestRenderEvalReportWithNoRecords(t *testing.T) {
	withStubbedRecords(t, nil, nil)

	out, _ := renderForTest(t)
	if !strings.Contains(out, "No eval runs recorded yet") {
		t.Errorf("expected the empty-history line:\n%s", out)
	}
}

func TestShortHash(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"1b4a0bec0ffee0dd", 7, "1b4a0be"},
		{"abc", 7, "abc"}, // shorter than n: returned whole, never panics
		{"", 7, ABSENT_VALUE},
	}
	for _, c := range cases {
		if got := shortHash(c.in, c.n); got != c.want {
			t.Errorf("shortHash(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}
