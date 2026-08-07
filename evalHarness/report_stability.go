package evalHarness

// This file is the second TRANSFORM stage of the eval-run aggregator (issue
// #37): report_group.go answers "how much does the SCORE move between runs",
// this answers "was it the same STORIES each run".
//
// The two questions are not the same, and the second is invisible in the first.
// Two runs of one configuration scoring 24 TP and 21 TP average to 22.5 whether
// the runs agreed on 21 stories and disagreed on 3, or agreed on 12 and
// disagreed on 21. The mean and the spread are identical; what they mean is
// opposite. In the first case the classifier reliably works on a solid core and
// reliably fails on a fixed set - a repeatable pattern worth going and fixing.
// In the second almost nothing is reliable and the mean is mostly luck.
//
// Splitting them also sizes the two kinds of remedial work. A relevant story no
// run ever catches will not be caught by re-running: it needs retrieval, prompt
// or corpus work. One that is caught sometimes has been caught before, so a
// sampling fix - a pinned temperature, or a verdict combined across runs - might
// convert it. Output 2 reports one lump FN covering both.
//
// Everything here is PURE - no I/O - for the same reason Evaluate and
// report_group.go are: the arithmetic is the part worth testing against
// hand-computed numbers.

// stabilityBuckets splits one class of stories by how consistently the runs
// flagged them. "Flagged" throughout means the run predicted 1, so for relevant
// stories these are catches and for irrelevant ones they are false alarms - the
// same computation answers both, which is why the irrelevant row is free.
type stabilityBuckets struct {
	Never     int // no run flagged it
	Sometimes int // some runs did, others did not
	Always    int // every run flagged it
}

// Total is the number of distinct stories in the class, for the row label. The
// buckets are exhaustive and mutually exclusive, so this cannot disagree with
// them.
func (b stabilityBuckets) Total() int {
	return b.Never + b.Sometimes + b.Always
}

// stabilityTable is one group's whole answer: the two rows and the n they were
// measured over. N is carried because the column headers state the run counts,
// and "1 of 2" against "1 of 7" are very different claims.
type stabilityTable struct {
	N          int
	Relevant   stabilityBuckets
	Irrelevant stabilityBuckets
}

// bucketStability counts, for each distinct story, how many of the group's runs
// flagged it, and buckets on that count.
//
// ⚠️ Stories are keyed on story_id, which COLLAPSES the dataset's 341 rows to
// 331 distinct stories: 10 ids appear twice. The collapse cannot lose or
// corrupt information, for two separate reasons - both verified on the real
// files:
//
//   - The duplicated ids all carry label 0, and both copies agree, so no
//     story lands in both classes.
//   - writePredictionsCSV derives `predicted` from predictedPositiveSet, a map
//     keyed on story_id, so the two rows of a duplicated id STRUCTURALLY cannot
//     disagree about the verdict.
//
// The visible consequence is that the irrelevant row totals 296 rather than 306.
// All 35 relevant stories are distinct, so nothing on the relevant side moves -
// which is where the conclusions get drawn.
func bucketStability(runs [][]storyVerdict) stabilityTable {
	labels := storyLabels(runs)
	timesFlagged := countRunsFlagging(runs)

	table := stabilityTable{N: len(runs)}
	for storyID, label := range labels {
		if label == 1 {
			table.Relevant.add(timesFlagged[storyID], len(runs))
		} else {
			table.Irrelevant.add(timesFlagged[storyID], len(runs))
		}
	}

	return table
}

// add files one story under never, sometimes or always. The comparison IS the
// bucketing: flagged by no run, by every run, or by neither extreme.
func (b *stabilityBuckets) add(timesFlagged, runs int) {
	switch timesFlagged {
	case 0:
		b.Never++
	case runs:
		b.Always++
	default:
		b.Sometimes++
	}
}

// storyLabels is every distinct story the group scored, with its class.
//
// It is the UNIVERSE, not merely a lookup - which is why the counts alone are
// not enough to build the table. countRunsFlagging only ever knows about stories
// somebody flagged, so the "never" bucket - stories nobody flagged - would be
// missing from it entirely and those stories would vanish silently. Every run in
// a group shares a dataset_hash by construction of the group key, so they cannot
// disagree about a story's label.
func storyLabels(runs [][]storyVerdict) map[int]int {
	labels := map[int]int{}
	for _, verdicts := range runs {
		for _, v := range verdicts {
			labels[v.StoryID] = v.Label
		}
	}
	return labels
}

// countRunsFlagging counts, per story, how many RUNS flagged it - at most one
// per run, however many rows that run carries for it.
//
// ⚠️ The per-run set is load-bearing, not tidiness. Ten story IDs appear twice
// in the dataset, so counting rows instead of runs would let a duplicated story
// reach 2n. That never equals n, so a story flagged by every single run would
// bucket as "sometimes" - silently wrong, in the direction that matters.
func countRunsFlagging(runs [][]storyVerdict) map[int]int {
	counts := map[int]int{}

	for _, verdicts := range runs {
		flaggedInThisRun := map[int]bool{}
		for _, v := range verdicts {
			if v.Predicted {
				flaggedInThisRun[v.StoryID] = true
			}
		}

		for storyID := range flaggedInThisRun {
			counts[storyID]++
		}
	}

	return counts
}
