package evalHarness

import (
	"math"
	"testing"
)

const floatTolerance = 1e-9

func assertClose(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > floatTolerance {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

// groupableRecord is an arm-2 record: every key field set except RerankModel,
// which only the reranking arm carries. Tests vary exactly one field and see
// whether it splits the group.
func groupableRecord(runID string) RunRecord {
	return RunRecord{
		RunID:           runID,
		Arm:             2,
		GitSHA:          "1b4a0be",
		DatasetHash:     "a59941fa34f7",
		CorpusIndexHash: "3daf2eee4e65",
		Model:           "claude-haiku-4-5-20251001",
		Metrics:         RunMetrics{TP: 24, FP: 79, TN: 227, FN: 11, Precision: 0.2330, Recall: 0.6857},
	}
}

func TestGroupRunsCollapsesIdenticalConfigurations(t *testing.T) {
	groups := groupRuns([]RunRecord{
		groupableRecord("20260806T084423Z-arm-2"),
		groupableRecord("20260806T085514Z-arm-2"),
	})

	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1: %+v", len(groups), groups)
	}
	if groups[0].N != 2 {
		t.Errorf("n = %d, want 2", groups[0].N)
	}
}

// Each key field must split a group on its own. Two runs that differed in any of
// them are two experiments, and averaging across them would report a real
// difference as if it were noise.
func TestGroupRunsSplitsOnEveryKeyField(t *testing.T) {
	cases := map[string]func(*RunRecord){
		"arm":               func(r *RunRecord) { r.Arm = 3 },
		"git_sha":           func(r *RunRecord) { r.GitSHA = "deadbee" },
		"model":             func(r *RunRecord) { r.Model = "claude-sonnet-5" },
		"rerank_model":      func(r *RunRecord) { r.RerankModel = "rerank-2.5-lite" },
		"dataset_hash":      func(r *RunRecord) { r.DatasetHash = "ffffffffffff" },
		"corpus_index_hash": func(r *RunRecord) { r.CorpusIndexHash = "eeeeeeeeeeee" },
	}

	for field, vary := range cases {
		t.Run(field, func(t *testing.T) {
			second := groupableRecord("20260806T085514Z-arm-2")
			vary(&second)

			groups := groupRuns([]RunRecord{groupableRecord("20260806T084423Z-arm-2"), second})
			if len(groups) != 2 {
				t.Fatalf("differing %s produced %d groups, want 2", field, len(groups))
			}
		})
	}
}

// Arms 0 and 1 record no corpus index hash. That shared absence is itself a
// configuration - "no index was read" - so those runs must group together rather
// than each becoming its own row.
func TestGroupRunsGroupsRunsWithoutACorpusIndex(t *testing.T) {
	first := groupableRecord("20260806T090351Z-arm-0")
	first.Arm, first.CorpusIndexHash = 0, ""
	second := groupableRecord("20260806T091200Z-arm-0")
	second.Arm, second.CorpusIndexHash = 0, ""

	groups := groupRuns([]RunRecord{first, second})
	if len(groups) != 1 || groups[0].N != 2 {
		t.Fatalf("arm-0 runs did not group on their shared absent index: %+v", groups)
	}
}

func TestGroupRunsMeanCounts(t *testing.T) {
	first := groupableRecord("20260806T084423Z-arm-2")
	first.Metrics = RunMetrics{TP: 24, FP: 79, TN: 227, FN: 11, Precision: 0.2330, Recall: 0.6857}
	second := groupableRecord("20260806T085514Z-arm-2")
	second.Metrics = RunMetrics{TP: 21, FP: 82, TN: 224, FN: 14, Precision: 0.2039, Recall: 0.6000}

	groups := groupRuns([]RunRecord{first, second})
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}

	g := groups[0]
	assertClose(t, "mean TP", g.MeanTP, 22.5)
	assertClose(t, "mean FP", g.MeanFP, 80.5)
	assertClose(t, "mean TN", g.MeanTN, 225.5)
	assertClose(t, "mean FN", g.MeanFN, 12.5)
}

// The AC that is easiest to "simplify" into wrongness later: precision and recall
// must be the mean of the per-run values, never recomputed from the summed
// counts.
//
// The fixture has to be constructed rather than borrowed from evalRuns/, and the
// reason is worth stating. Recall's denominator (TP+FN) is the dataset's positive
// count, constant across every run on one dataset, so mean-of-ratios and
// ratio-of-means can never disagree for recall. Only precision's denominator
// (TP+FP - how many stories the run flagged) varies, and the real records happen
// to have flagged the same number. A pooled implementation would pass against
// live data.
func TestGroupRunsAveragesRatesPerRunNotPooled(t *testing.T) {
	// Run A flags 20 stories and gets 10 right (precision 0.50).
	first := groupableRecord("20260806T084423Z-arm-2")
	first.Metrics = RunMetrics{TP: 10, FP: 10, TN: 296, FN: 25, Precision: 0.50, Recall: 10.0 / 35.0}
	// Run B flags 40 and gets 30 right (precision 0.75).
	second := groupableRecord("20260806T085514Z-arm-2")
	second.Metrics = RunMetrics{TP: 30, FP: 10, TN: 296, FN: 5, Precision: 0.75, Recall: 30.0 / 35.0}

	groups := groupRuns([]RunRecord{first, second})
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}

	// Mean of ratios: (0.50 + 0.75) / 2. Ratio of means would be 40/60 = 0.6667.
	assertClose(t, "mean precision", groups[0].Precision.Mean, 0.625)

	// Recall agrees under both formulas here (40/70 == mean of 10/35 and 30/35),
	// which is precisely why precision is the one that pins the rule.
	assertClose(t, "mean recall", groups[0].Recall.Mean, (10.0/35.0+30.0/35.0)/2)
}

// Hand-computed, and chosen so the sample (n-1) and population (n) answers
// differ: mean 5, sum of squared deviations 32, so sample = sqrt(32/7) and
// population = sqrt(32/8) = 2. A regression to the population form must fail this
// test, not squeak past it.
func TestSampleStdDevUsesNMinusOne(t *testing.T) {
	values := []float64{2, 4, 4, 4, 5, 5, 7, 9}

	got, defined := sampleStdDev(values)
	if !defined {
		t.Fatal("sampleStdDev reported undefined for 8 values")
	}
	assertClose(t, "sample stddev", got, math.Sqrt(32.0/7.0))

	if math.Abs(got-2.0) < 1e-6 {
		t.Error("sampleStdDev returned the POPULATION standard deviation (n denominator)")
	}
}

// One run has no spread to measure. Returning `false` rather than 0 is what stops
// the report printing "± 0.0000", which reads as measured certainty.
func TestSampleStdDevUndefinedBelowTwoValues(t *testing.T) {
	for _, values := range [][]float64{nil, {}, {0.2330}} {
		if _, defined := sampleStdDev(values); defined {
			t.Errorf("sampleStdDev(%v) reported defined, want undefined", values)
		}
	}
}

func TestSummarizeCarriesTheDefinedFlag(t *testing.T) {
	single := summarize([]float64{0.2330})
	assertClose(t, "single-value mean", single.Mean, 0.2330)
	if single.StdDevDefined {
		t.Error("n=1 aggregate claims a defined standard deviation")
	}

	pair := summarize([]float64{0.2330, 0.2039})
	assertClose(t, "pair mean", pair.Mean, 0.21845)
	if !pair.StdDevDefined {
		t.Error("n=2 aggregate reports an undefined standard deviation")
	}
}

func TestMean(t *testing.T) {
	assertClose(t, "mean of empty", mean(nil), 0)
	assertClose(t, "mean", mean([]float64{1, 2, 6}), 3)
}

// Sorted by n descending, then by the key fields. The tiebreak is not cosmetic:
// Go randomises map iteration, so without a total ordering the report would
// differ between two runs over identical records.
func TestGroupRunsOrdering(t *testing.T) {
	arm3 := groupableRecord("20260806T090525Z-arm-3")
	arm3.Arm = 3
	arm0 := groupableRecord("20260806T090351Z-arm-0")
	arm0.Arm, arm0.CorpusIndexHash = 0, ""

	// Insertion order deliberately unlike the expected output order.
	groups := groupRuns([]RunRecord{
		arm3,
		groupableRecord("20260806T084423Z-arm-2"),
		arm0,
		groupableRecord("20260806T085514Z-arm-2"),
	})

	wantArms := []int{2, 0, 3} // n=2 first, then the singletons by arm ascending
	if len(groups) != len(wantArms) {
		t.Fatalf("got %d groups, want %d", len(groups), len(wantArms))
	}
	for i, want := range wantArms {
		if groups[i].Key.Arm != want {
			t.Errorf("group %d is arm %d, want arm %d", i, groups[i].Key.Arm, want)
		}
	}
	if groups[0].N != 2 {
		t.Errorf("first group n = %d, want 2", groups[0].N)
	}
}

// TestGroupRunsOrdersByRerankModel: a key field without a tiebreak reintroduces
// the nondeterminism the total ordering exists to prevent. Two groups differing
// ONLY in the rerank model reach the tiebreak chain's new link, and must come
// back in the same order however the records arrived.
func TestGroupRunsOrdersByRerankModel(t *testing.T) {
	lite := groupableRecord("20260808T090000Z-arm-4")
	lite.Arm, lite.RerankModel = 4, "rerank-2.5-lite"
	full := groupableRecord("20260808T100000Z-arm-4")
	full.Arm, full.RerankModel = 4, "rerank-2.5"

	for _, records := range [][]RunRecord{{lite, full}, {full, lite}} {
		groups := groupRuns(records)
		if len(groups) != 2 {
			t.Fatalf("got %d groups, want 2 (the rerank model is a key field)", len(groups))
		}
		if groups[0].Key.RerankModel != "rerank-2.5" || groups[1].Key.RerankModel != "rerank-2.5-lite" {
			t.Errorf("order = [%s %s], want ascending by rerank model whatever the input order",
				groups[0].Key.RerankModel, groups[1].Key.RerankModel)
		}
	}
}

func TestGroupRunsCarriesItsMemberRunIDs(t *testing.T) {
	arm3 := groupableRecord("20260806T090525Z-arm-3")
	arm3.Arm = 3

	groups := groupRuns([]RunRecord{
		groupableRecord("20260806T084423Z-arm-2"),
		arm3,
		groupableRecord("20260806T085514Z-arm-2"),
	})

	want := map[int][]string{
		2: {"20260806T084423Z-arm-2", "20260806T085514Z-arm-2"},
		3: {"20260806T090525Z-arm-3"},
	}
	for _, g := range groups {
		expected := want[g.Key.Arm]
		if len(g.RunIDs) != len(expected) {
			t.Fatalf("arm %d group has run IDs %v, want %v", g.Key.Arm, g.RunIDs, expected)
		}
		for i, id := range expected {
			if g.RunIDs[i] != id {
				t.Errorf("arm %d group run ID %d = %q, want %q", g.Key.Arm, i, g.RunIDs[i], id)
			}
		}
	}
}

// The run IDs must not depend on the order the records arrived in: the stability
// section reads a CSV per member, and a report that reorders between two
// invocations over identical records is untestable.
func TestGroupRunsSortsRunIDsRegardlessOfInputOrder(t *testing.T) {
	groups := groupRuns([]RunRecord{
		groupableRecord("20260806T085514Z-arm-2"),
		groupableRecord("20260806T084423Z-arm-2"),
	})

	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	want := []string{"20260806T084423Z-arm-2", "20260806T085514Z-arm-2"}
	for i, id := range want {
		if groups[0].RunIDs[i] != id {
			t.Errorf("run ID %d = %q, want %q (run IDs: %v)", i, groups[0].RunIDs[i], id, groups[0].RunIDs)
		}
	}
}

func TestGroupRunsKeepsNInStepWithRunIDs(t *testing.T) {
	groups := groupRuns([]RunRecord{
		groupableRecord("20260806T084423Z-arm-2"),
		groupableRecord("20260806T085514Z-arm-2"),
	})

	if groups[0].N != len(groups[0].RunIDs) {
		t.Errorf("n = %d but %d run IDs carried", groups[0].N, len(groups[0].RunIDs))
	}
}
