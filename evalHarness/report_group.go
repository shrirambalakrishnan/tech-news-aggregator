package evalHarness

import (
	"math"
	"sort"
)

// This file is the TRANSFORM stage of the eval-run aggregator (issue #34): it
// answers "how much does one configuration's score move between runs".
//
// A single eval run is a POINT ESTIMATE, not a measurement. The classifier is
// non-deterministic - claudeapi.Request sends no temperature field, so the API
// default of 1.0 applies - and at 35 positives the binomial standard error on
// recall is already ~8pp. So "arm X beat arm Y by 11pp" is not a result until the
// run-to-run spread is known. CLAUDE.md's Approach 5 records exactly one such
// spread (0.343 ± 0.076), and it was computed by hand.
//
// Grouping is what makes the spread meaningful: two runs are repeats of the same
// experiment only if they shared the arm, the code, the model, the dataset and
// the corpus index. Any of those differing makes them two experiments, and
// averaging across them would report a difference as if it were noise.
//
// Everything here is PURE - no I/O - for the same reason Evaluate is: the
// statistics are the part worth unit-testing against hand-computed numbers.

// runGroupKey is the tuple that identifies one configuration. A comparable
// struct, used directly as the map key, rather than a joined string: a separator
// in a joined key is one model name away from colliding two configurations into
// one row, and a collision here silently understates variance.
//
// CorpusIndexHash is empty for arms 0 and 1, which never load the index, and
// RerankModel is empty for arms 0-3, which never rerank. Those empty values group
// their runs together correctly - they genuinely share "no index" / "no reranker"
// as a configuration - and are rendered as absent, not as a value, at print time.
// Records written before the rerank model was recorded decode to "" too, so they
// group exactly as they did before the field existed.
type runGroupKey struct {
	Arm             int
	GitSHA          string
	Model           string
	RerankModel     string
	DatasetHash     string
	CorpusIndexHash string
}

// aggregate is one metric's mean plus its spread across the group's runs.
//
// StdDevDefined is what makes "not measured" representable at all. A bare float64
// would force the n=1 case to carry 0.0, which prints as "± 0.0000" and reads as
// perfect reproducibility - the exact opposite of the truth, which is that a
// single run says nothing about spread. n=1 is the common case here, not an edge
// case: three of the four records on disk today are the only run of their
// configuration.
type aggregate struct {
	Mean          float64
	StdDev        float64
	StdDevDefined bool
}

// runGroup is one row of the variance table: a configuration, how many runs were
// measured under it, and what they scored.
//
// The counts carry no spread and the rates do, deliberately. TP/FP/TN/FN are
// there to describe the configuration's typical behaviour; precision and recall
// are the numbers decisions get made on, so they are the ones whose stability has
// to be visible.
//
// RunIDs names the members. The means say what the configuration scored; they
// cannot say WHICH runs produced them, and the per-story stability section
// (issue #37) has to open each member's predictions CSV by run ID. N is derived
// from this slice rather than counted separately, so the two cannot drift.
type runGroup struct {
	Key                            runGroupKey
	RunIDs                         []string
	N                              int
	MeanTP, MeanFP, MeanTN, MeanFN float64
	Precision, Recall              aggregate
}

// groupRuns collapses run records into one row per configuration, sorted by n
// descending - the groups with something to say about variance first - then by
// the key fields ascending.
//
// The tiebreak is required, not cosmetic: Go randomises map iteration order, so
// without a total ordering the report would differ between two invocations over
// identical records, and would be untestable.
//
// ⚠️ Precision and recall are averaged PER RUN, never recomputed from the summed
// counts. Mean-of-ratios is not ratio-of-means, and the pooled form would be
// worse than merely different: pooling sums the numerators and denominators, so
// the per-run variation cancels inside the fraction - it would average away
// exactly the quantity this table exists to measure. (The trap is well hidden for
// recall, whose denominator TP+FN is the dataset's positive count and so is
// constant across runs on one dataset, making both formulas agree; only
// precision's denominator, how many stories the run flagged, actually varies.)
func groupRuns(records []RunRecord) []runGroup {
	type accumulator struct {
		runIDs              []string
		tp, fp, tn, fn      []float64
		precisions, recalls []float64
	}

	order := []runGroupKey{}
	byKey := map[runGroupKey]*accumulator{}

	for _, r := range records {
		key := runGroupKey{
			Arm:             r.Arm,
			GitSHA:          r.GitSHA,
			Model:           r.Model,
			RerankModel:     r.RerankModel,
			DatasetHash:     r.DatasetHash,
			CorpusIndexHash: r.CorpusIndexHash,
		}
		acc, seen := byKey[key]
		if !seen {
			acc = &accumulator{}
			byKey[key] = acc
			order = append(order, key)
		}

		acc.runIDs = append(acc.runIDs, r.RunID)
		acc.tp = append(acc.tp, float64(r.Metrics.TP))
		acc.fp = append(acc.fp, float64(r.Metrics.FP))
		acc.tn = append(acc.tn, float64(r.Metrics.TN))
		acc.fn = append(acc.fn, float64(r.Metrics.FN))
		acc.precisions = append(acc.precisions, r.Metrics.Precision)
		acc.recalls = append(acc.recalls, r.Metrics.Recall)
	}

	groups := make([]runGroup, 0, len(order))
	for _, key := range order {
		acc := byKey[key]

		// Sorted for the same reason sortGroups exists: a caller must not be
		// able to change the report by handing the records over in a different
		// order. loadRunRecords already sorts, so this is normally a no-op -
		// but groupRuns is also called directly, and downstream output keyed on
		// this slice has to be reproducible.
		sort.Strings(acc.runIDs)

		groups = append(groups, runGroup{
			Key:       key,
			RunIDs:    acc.runIDs,
			N:         len(acc.runIDs),
			MeanTP:    mean(acc.tp),
			MeanFP:    mean(acc.fp),
			MeanTN:    mean(acc.tn),
			MeanFN:    mean(acc.fn),
			Precision: summarize(acc.precisions),
			Recall:    summarize(acc.recalls),
		})
	}

	sortGroups(groups)
	return groups
}

// sortGroups imposes the total ordering: n descending, then every key field
// ascending so the result is deterministic whatever order the records arrived in.
func sortGroups(groups []runGroup) {
	sort.Slice(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.N != b.N {
			return a.N > b.N
		}
		if a.Key.Arm != b.Key.Arm {
			return a.Key.Arm < b.Key.Arm
		}
		if a.Key.GitSHA != b.Key.GitSHA {
			return a.Key.GitSHA < b.Key.GitSHA
		}
		if a.Key.Model != b.Key.Model {
			return a.Key.Model < b.Key.Model
		}
		if a.Key.RerankModel != b.Key.RerankModel {
			return a.Key.RerankModel < b.Key.RerankModel
		}
		if a.Key.DatasetHash != b.Key.DatasetHash {
			return a.Key.DatasetHash < b.Key.DatasetHash
		}
		return a.Key.CorpusIndexHash < b.Key.CorpusIndexHash
	})
}

// summarize reduces one metric's per-run values to its mean and spread.
func summarize(values []float64) aggregate {
	stdDev, defined := sampleStdDev(values)
	return aggregate{Mean: mean(values), StdDev: stdDev, StdDevDefined: defined}
}

// mean is the arithmetic mean, 0 for an empty slice.
func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// sampleStdDev is the SAMPLE standard deviation (n-1 denominator), and reports
// whether it is defined at all.
//
// Sample, not population: these runs are draws from a non-deterministic process
// that could have produced others, not the complete set of runs that exist. The
// population form (n denominator) would understate the spread, and understating
// it is the failure that matters - it makes a difference inside the noise look
// like a result.
//
// Undefined below n=2: one run has no spread to measure. The bool is returned
// rather than 0 so the caller cannot print a fabricated "± 0.0000".
func sampleStdDev(values []float64) (float64, bool) {
	if len(values) < 2 {
		return 0, false
	}

	m := mean(values)
	sumSquares := 0.0
	for _, v := range values {
		d := v - m
		sumSquares += d * d
	}
	return math.Sqrt(sumSquares / float64(len(values)-1)), true
}
