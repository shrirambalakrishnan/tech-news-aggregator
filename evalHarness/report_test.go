package evalHarness

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// withStubbedRecords swaps the loader seam so the rendering tests never touch
// disk, and restores it afterwards.
//
// It stubs the predictions seam too. The report now reads a CSV per member run
// of every repeated group, and evalRuns/ is git-ignored - so a rendering test
// left on the real seam would pass locally off whatever runs happen to be on
// disk, and fail in CI where there are none.
func withStubbedRecords(t *testing.T, records []RunRecord, skipped []error) {
	t.Helper()

	original := loadRecords
	loadRecords = func(string) ([]RunRecord, []error) { return records, skipped }
	t.Cleanup(func() { loadRecords = original })

	withStubbedPredictions(t, nil)
}

// withStubbedPredictions serves each run's verdicts from a map. An unlisted run
// yields no rows rather than an error, so a test only has to describe the runs
// it cares about.
func withStubbedPredictions(t *testing.T, byRunID map[string][]storyVerdict) {
	t.Helper()

	original := loadPredictions
	loadPredictions = func(_, runID string) ([]storyVerdict, error) { return byRunID[runID], nil }
	t.Cleanup(func() { loadPredictions = original })
}

// withFailingPredictions makes one run's CSV unreadable, to exercise the skip
// path.
func withFailingPredictions(t *testing.T, failingRunID string) {
	t.Helper()

	original := loadPredictions
	loadPredictions = func(_, runID string) ([]storyVerdict, error) {
		if runID == failingRunID {
			return nil, errors.New("failed to open predictions CSV: no such file or directory")
		}
		return nil, nil
	}
	t.Cleanup(func() { loadPredictions = original })
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

// The rerank model is a grouping key, so Output 1 prints it whole. Abbreviating
// to the 12-character hash width would render rerank-2.5-lite as "rerank-2.5-l":
// not a model Voyage has, and stripped of the exact suffix that distinguishes it
// from rerank-2.5.
func TestFormatRunsTablePrintsRerankModelInFull(t *testing.T) {
	record := testRecord("20260806T084423Z-arm-4", 4)
	record.RerankModel = "rerank-2.5-lite"

	table := formatRunsTable([]RunRecord{record})
	if !strings.Contains(table, "rerank_model") {
		t.Errorf("runs table missing the rerank_model column:\n%s", table)
	}
	if !strings.Contains(table, "rerank-2.5-lite") {
		t.Errorf("table must print the rerank model in full:\n%s", table)
	}
}

// Arms 0-3 never rerank. That absence must render as ABSENT_VALUE, for the same
// reason the corpus index hash does: a blank cell reads as "reranked with
// nothing" rather than "did not rerank".
func TestFormatRunsTableMarksAbsentRerankModel(t *testing.T) {
	record := testRecord("20260806T084423Z-arm-2", 2)
	record.RerankModel = ""

	table := formatRunsTable([]RunRecord{record})
	if strings.Count(table, ABSENT_VALUE) != 1 {
		t.Errorf("expected exactly one %q (the absent rerank model):\n%s", ABSENT_VALUE, table)
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
	groupsAt := strings.Index(out, "=== Grouped by (arm, config, model, rerank_model, dataset_hash, corpus_index_hash) — 1 group ===")
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

// The legend is what keeps a fixed-width hash column readable. One entry per
// distinct configuration - not per run - and a pre-config record says so rather
// than sitting there as a config hash whose expansion went missing.
func TestConfigLegendExpandsEachDistinctConfig(t *testing.T) {
	first := groupableRecord("20260806T084423Z-arm-2")
	repeat := groupableRecord("20260806T085514Z-arm-2")
	other := groupableRecord("20260806T090525Z-arm-3")
	other.Config = map[string]string{"eval_batch_size": "30", "retrieval_pool_cap": "20"}
	legacy := groupableRecord("20260805T090000Z-arm-2")
	legacy.Config = nil

	entries := configLegend([]RunRecord{first, repeat, other, legacy})

	if len(entries) != 3 {
		t.Fatalf("got %d legend entries, want 3 (two configs + one pre-config): %+v", len(entries), entries)
	}

	var configs []string
	for _, e := range entries {
		configs = append(configs, e.Config)
	}
	joined := strings.Join(configs, "\n")

	// Keys ascending, so two printings of one config always read the same.
	if !strings.Contains(joined, "eval_batch_size=30 retrieval_top_k=5") {
		t.Errorf("legend missing the arm-2 config, keys ascending:\n%s", joined)
	}
	if !strings.Contains(joined, "eval_batch_size=30 retrieval_pool_cap=20") {
		t.Errorf("legend missing the second config:\n%s", joined)
	}
	if !strings.Contains(joined, PRE_CONFIG_NOTE) {
		t.Errorf("a record with no config must say so, not render as an empty config:\n%s", joined)
	}
}

// The display contract after issue #40: config identifies a configuration
// everywhere, and git_sha survives only as per-run provenance. A group can now
// span commits, so a sha printed beside a group would name one arbitrary member
// as though it spoke for all of them.
func TestRenderEvalReportShowsConfigEverywhereAndGitSHAPerRunOnly(t *testing.T) {
	first := groupableRecord("20260806T084423Z-arm-2")
	second := groupableRecord("20260807T204405Z-arm-2")
	second.GitSHA = "74f5520deadbeef"
	withStubbedRecords(t, []RunRecord{first, second}, nil)

	out, _ := renderForTest(t)

	runsTable := out[:strings.Index(out, "=== Grouped by")]
	rest := out[strings.Index(out, "=== Grouped by"):]

	// Output 1 keeps both shas: they are what a reader pastes into `git show`.
	for _, sha := range []string{"1b4a0be", "74f5520"} {
		if !strings.Contains(runsTable, sha) {
			t.Errorf("Output 1 must still carry git_sha %q:\n%s", sha, runsTable)
		}
		if strings.Contains(rest, sha) {
			t.Errorf("git_sha %q leaked into the grouped/stability output:\n%s", sha, rest)
		}
	}

	// The two runs share a config, so they are one group despite the two shas.
	if !strings.Contains(rest, "— 1 group ===") {
		t.Errorf("two shas with one config should be one group:\n%s", rest)
	}

	config := shortHash(fingerprint(first), CONTENT_HASH_SHORT_LEN)
	if !strings.Contains(runsTable, config) || !strings.Contains(rest, config) {
		t.Errorf("the config fingerprint %q must appear in both tables:\n%s", config, out)
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

// stabilityRecords are two runs of one configuration plus a singleton, so a test
// can check both that a repeated group gets a block and that a lone run does not.
func stabilityRecords() []RunRecord {
	arm3 := testRecord("20260806T090525Z-arm-3", 3)
	return []RunRecord{
		testRecord("20260806T084423Z-arm-2", 2),
		testRecord("20260806T085514Z-arm-2", 2),
		arm3,
	}
}

func TestRenderEvalReportPrintsOneStabilityBlockPerRepeatedGroup(t *testing.T) {
	withStubbedRecords(t, stabilityRecords(), nil)
	withStubbedPredictions(t, map[string][]storyVerdict{
		"20260806T084423Z-arm-2": {
			{StoryID: 1, Label: 1, Predicted: true},
			{StoryID: 2, Label: 1, Predicted: true},
			{StoryID: 3, Label: 0, Predicted: false},
		},
		"20260806T085514Z-arm-2": {
			{StoryID: 1, Label: 1, Predicted: true},
			{StoryID: 2, Label: 1, Predicted: false},
			{StoryID: 3, Label: 0, Predicted: false},
		},
	})

	out, warn := renderForTest(t)

	if got := strings.Count(out, "Per-story stability"); got != 1 {
		t.Fatalf("got %d stability blocks, want 1 - only the arm-2 group has 2 runs:\n%s", got, out)
	}
	if !strings.Contains(out, "arm 2,") {
		t.Errorf("the block does not name arm 2:\n%s", out)
	}
	if !strings.Contains(out, "(n=2 runs)") {
		t.Errorf("the block does not state n:\n%s", out)
	}
	if warn != "" {
		t.Errorf("unexpected warnings: %q", warn)
	}
}

// One run measures no stability: every story it flagged was flagged by every
// run, so a block would read as perfect consistency. Same rule as the n=1 no-±
// convention.
func TestRenderEvalReportPrintsNoStabilityBlockForSingleRunGroups(t *testing.T) {
	withStubbedRecords(t, []RunRecord{testRecord("20260806T090351Z-arm-0", 0)}, nil)

	out, _ := renderForTest(t)
	if strings.Contains(out, "Per-story stability") {
		t.Errorf("a lone run produced a stability block:\n%s", out)
	}
}

// The stability section is an addition below the existing report, not a
// rearrangement of it.
func TestRenderEvalReportPlacesStabilityAfterTheGroupsTable(t *testing.T) {
	withStubbedRecords(t, stabilityRecords(), nil)

	out, _ := renderForTest(t)
	groups := strings.Index(out, "=== Grouped by")
	stability := strings.Index(out, "Per-story stability")
	if groups < 0 || stability < 0 {
		t.Fatalf("expected both sections:\n%s", out)
	}
	if stability < groups {
		t.Errorf("the stability section printed before the groups table:\n%s", out)
	}
}

// A block computed from some of a group's runs would carry a header saying n=2
// over counts measured across one. Skip the group, name it, keep the rest.
func TestRenderEvalReportSkipsAGroupWithUnreadablePredictions(t *testing.T) {
	withStubbedRecords(t, stabilityRecords(), nil)
	withFailingPredictions(t, "20260806T085514Z-arm-2")

	out, warn := renderForTest(t)

	if strings.Contains(out, "Per-story stability") {
		t.Errorf("a block was printed from a partially readable group:\n%s", out)
	}
	if !strings.Contains(warn, "arm 2") {
		t.Errorf("the warning does not name the skipped group: %q", warn)
	}
	if !strings.Contains(out, "=== Eval runs (3) ===") || !strings.Contains(out, "=== Grouped by") {
		t.Errorf("the rest of the report was lost with the block:\n%s", out)
	}
}

func TestFormatStabilityTableCountsAndTotals(t *testing.T) {
	table := formatStabilityTable(stabilityTable{
		N:          2,
		Relevant:   stabilityBuckets{Never: 11, Sometimes: 3, Always: 21},
		Irrelevant: stabilityBuckets{Never: 211, Sometimes: 11, Always: 74},
	})

	for _, want := range []string{"relevant (35)", "irrelevant (296)", "11", "3", "21", "211", "74"} {
		if !strings.Contains(table, want) {
			t.Errorf("table missing %q:\n%s", want, table)
		}
	}
}

// "Sometimes" is a far weaker claim at n=2, where it can only mean 1 of 2, than
// at n=3. The column has to say which.
func TestRunCountLabelStatesTheRunCounts(t *testing.T) {
	tests := []struct {
		low, high, n int
		want         string
	}{
		{0, 0, 2, "(0 of 2)"},
		{1, 1, 2, "(1 of 2)"},
		{2, 2, 2, "(2 of 2)"},
		{1, 2, 3, "(1-2 of 3)"},
		{3, 3, 3, "(3 of 3)"},
	}

	for _, test := range tests {
		if got := runCountLabel(test.low, test.high, test.n); got != test.want {
			t.Errorf("runCountLabel(%d, %d, %d) = %q, want %q", test.low, test.high, test.n, got, test.want)
		}
	}
}

func TestFormatStabilityTableAdaptsColumnsToN(t *testing.T) {
	table := formatStabilityTable(stabilityTable{N: 3})
	for _, want := range []string{"(0 of 3)", "(1-2 of 3)", "(3 of 3)"} {
		if !strings.Contains(table, want) {
			t.Errorf("n=3 table missing %q:\n%s", want, table)
		}
	}
}

// Same rule as the runs table: an arm-0 or arm-1 group has no corpus index, and
// a blank field there reads as an empty index rather than as no index.
func TestStabilityHeaderMarksAbsentCorpusIndex(t *testing.T) {
	header := stabilityHeader(runGroup{
		Key: runGroupKey{Arm: 0, ConfigFingerprint: "c0nf1g1dent1ty", Model: "claude-haiku-4-5-20251001"},
		N:   2,
	})

	// Anchored to the labelled field, not to a bare ABSENT_VALUE: the header's
	// own "stability — arm" separator is an em dash, so a bare Contains check
	// passes whatever the index field renders as - including nothing at all.
	if !strings.Contains(header, "index "+ABSENT_VALUE) {
		t.Errorf("expected %q for the absent corpus index hash: %q", ABSENT_VALUE, header)
	}
	// The model is a grouping key, so it is never abbreviated.
	if !strings.Contains(header, "claude-haiku-4-5-20251001") {
		t.Errorf("header must print the model in full: %q", header)
	}
}

// Output 3's header names the configuration, and the reranker is part of that
// configuration now that it is part of the key.
func TestStabilityHeaderNamesTheRerankModel(t *testing.T) {
	reranked := stabilityHeader(runGroup{
		Key: runGroupKey{
			Arm: 4, ConfigFingerprint: "c0nf1g1dent1ty",
			Model: "claude-haiku-4-5-20251001", RerankModel: "rerank-2.5-lite",
			DatasetHash: "a59941fa34f7", CorpusIndexHash: "3daf2eee4e65",
		},
		N: 2,
	})
	if !strings.Contains(reranked, "rerank-2.5-lite") {
		t.Errorf("arm-4 header must name the reranker in full: %q", reranked)
	}

	plain := stabilityHeader(runGroup{
		Key: runGroupKey{
			Arm: 2, ConfigFingerprint: "c0nf1g1dent1ty",
			Model: "claude-haiku-4-5-20251001",
			// No RerankModel: arm 2 does not rerank.
			DatasetHash: "a59941fa34f7", CorpusIndexHash: "3daf2eee4e65",
		},
		N: 2,
	})
	// Same anchoring as the corpus-index test above, and for the same reason: the
	// header contains an em dash unconditionally, so `Contains(plain,
	// ABSENT_VALUE)` would still pass with the rerank field dropped entirely.
	if !strings.Contains(plain, "rerank "+ABSENT_VALUE) {
		t.Errorf("arm-2 header should mark the absent reranker with %q: %q", ABSENT_VALUE, plain)
	}
}

func TestOrAbsent(t *testing.T) {
	if got := orAbsent(""); got != ABSENT_VALUE {
		t.Errorf("orAbsent(\"\") = %q, want %q", got, ABSENT_VALUE)
	}
	// Never truncated, however long: the value is a grouping key.
	if got := orAbsent("rerank-2.5-lite"); got != "rerank-2.5-lite" {
		t.Errorf("orAbsent left the value untouched? got %q", got)
	}
}
