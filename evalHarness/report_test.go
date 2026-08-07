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

func TestFormatAggregateShowsSpreadOnlyWhenMeasured(t *testing.T) {
	measured := formatAggregate(aggregate{Mean: 0.2184, StdDev: 0.0206, StdDevDefined: true})
	if measured != "0.2184 ± 0.0206" {
		t.Errorf("formatAggregate(n>1) = %q, want %q", measured, "0.2184 ± 0.0206")
	}

	// The rule this whole aggregate type exists for: at n=1 nothing is known
	// about the spread, and "± 0.0000" would claim perfect reproducibility.
	single := formatAggregate(aggregate{Mean: 0.1269})
	if single != "0.1269" {
		t.Errorf("formatAggregate(n=1) = %q, want %q", single, "0.1269")
	}
	if strings.Contains(single, "±") {
		t.Errorf("n=1 rendered a ±: %q", single)
	}
}

func TestFormatGroupsTableRowsCarryNAndSpread(t *testing.T) {
	repeated := groupableRecord("20260806T084423Z-arm-2")
	repeated.Metrics = RunMetrics{TP: 24, FP: 79, TN: 227, FN: 11, Precision: 0.2330, Recall: 0.6857}
	second := groupableRecord("20260806T085514Z-arm-2")
	second.Metrics = RunMetrics{TP: 21, FP: 82, TN: 224, FN: 14, Precision: 0.2039, Recall: 0.6000}
	single := groupableRecord("20260806T090351Z-arm-0")
	single.Arm, single.CorpusIndexHash = 0, ""
	single.Metrics = RunMetrics{TP: 17, FP: 117, TN: 189, FN: 18, Precision: 0.1269, Recall: 0.4857}

	table := formatGroupsTable(groupRuns([]RunRecord{repeated, second, single}))
	lines := strings.Split(strings.TrimRight(table, "\n"), "\n")
	if len(lines) != 3 { // header + two groups
		t.Fatalf("table has %d lines, want 3:\n%s", len(lines), table)
	}

	// n=2 first (sorted by n descending), with mean counts to one decimal and a
	// measured spread on both rates.
	grouped := lines[1]
	for _, want := range []string{"22.5", "80.5", "225.5", "12.5", "0.2185 ± 0.0206"} {
		if !strings.Contains(grouped, want) {
			t.Errorf("n=2 row missing %q:\n%s", want, grouped)
		}
	}

	// n=1 second, with no ± anywhere on the row.
	lone := lines[2]
	if !strings.Contains(lone, "0.1269") || !strings.Contains(lone, "0.4857") {
		t.Errorf("n=1 row missing its rates:\n%s", lone)
	}
	if strings.Contains(lone, "±") {
		t.Errorf("n=1 row printed a ±, which reads as measured certainty:\n%s", lone)
	}
	if !strings.Contains(lone, ABSENT_VALUE) {
		t.Errorf("arm-0 group should mark its absent corpus index:\n%s", lone)
	}
}

// Both sections, in order, from one call - the shape an operator actually sees.
func TestRenderEvalReportPrintsBothSections(t *testing.T) {
	first := groupableRecord("20260806T084423Z-arm-2")
	second := groupableRecord("20260806T085514Z-arm-2")
	withStubbedRecords(t, []RunRecord{first, second}, nil)

	out, _ := renderForTest(t)

	runsAt := strings.Index(out, "=== Eval runs (2) ===")
	groupsAt := strings.Index(out, "=== Grouped by (arm, git_sha, model, dataset_hash, corpus_index_hash) — 1 group ===")
	if runsAt < 0 {
		t.Fatalf("runs section missing:\n%s", out)
	}
	if groupsAt < 0 {
		t.Fatalf("groups section missing:\n%s", out)
	}
	if groupsAt < runsAt {
		t.Errorf("groups section printed before the runs section:\n%s", out)
	}

	// Every run appears once in the first table; the group appears once in the
	// second.
	if strings.Count(out, first.RunID) != 1 || strings.Count(out, second.RunID) != 1 {
		t.Errorf("each run should appear exactly once:\n%s", out)
	}
	if !strings.Contains(out, "n=1 groups show no ±") {
		t.Errorf("footnote missing from the report:\n%s", out)
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
