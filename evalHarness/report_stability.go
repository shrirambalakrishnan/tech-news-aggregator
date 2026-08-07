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
	// Collapsed ONCE, at the top, so every step below can assume one row per
	// story per run and none of them has to restate the rule.
	deduped := dedupeStories(runs)

	scored := scoredStories(deduped)
	timesFlagged := countRunsFlagging(deduped)

	table := stabilityTable{N: len(runs)}
	for storyID, label := range scored {
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

// scoredStories is the set of every story the group scored, carrying each one's
// class along as the value.
//
// ENUMERATION is the point, not lookup - which is why the flag counts alone
// cannot build the table. countRunsFlagging knows only about stories somebody
// flagged, so a story no run flagged has no key there at all: it is absent, not
// zero. "Never" is a bucket this table prints, so those stories have to be
// enumerated from somewhere else or they vanish and the row totals come up
// short.
//
// The label write is idempotent, not a race: every run in a group shares a
// dataset_hash by construction of the group key, so each run writes the same
// label for the same story. The loop is a union over the runs, and the last
// write agrees with the first.
func scoredStories(runs [][]storyVerdict) map[int]int {
	stories := map[int]int{}
	for _, verdicts := range runs {
		for _, v := range verdicts {
			stories[v.StoryID] = v.Label
		}
	}
	return stories
}

// countRunsFlagging counts, per story, how many runs flagged it. Takes deduped
// runs, so one row is one run's verdict and a plain increment is correct.
func countRunsFlagging(runs [][]storyVerdict) map[int]int {
	counts := map[int]int{}

	for _, verdicts := range runs {
		for _, v := range verdicts {
			if v.Predicted {
				counts[v.StoryID]++
			}
		}
	}

	return counts
}

// dedupeStories collapses each run's rows to one per story, keeping the first
// and preserving order.
//
// ⚠️ This is the only place duplicates are handled, and it is load-bearing
// rather than tidiness. Ten story IDs appear twice in the dataset, so counting
// rows instead of stories would let a duplicated story reach 2n flags. That
// never equals n, so a story flagged by every single run would bucket as
// "sometimes" - silently wrong, in the direction that matters.
//
// Keeping the FIRST row is safe rather than arbitrary: the duplicated IDs all
// carry label 0 and agree, and writePredictionsCSV derives `predicted` from a
// map keyed on story_id, so two rows for one story structurally cannot disagree
// about the verdict either.
//
// Deliberately NOT done in loadPredictionsCSV. The run record's confusion matrix
// is computed over the CSV's 341 ROWS, so a loader that dropped rows would make
// a CSV impossible to reconcile against the record beside it - which is the
// property the CSV was written for.
func dedupeStories(runs [][]storyVerdict) [][]storyVerdict {
	deduped := make([][]storyVerdict, 0, len(runs))

	for _, verdicts := range runs {
		seen := make(map[int]bool, len(verdicts))
		distinct := make([]storyVerdict, 0, len(verdicts))

		for _, v := range verdicts {
			if seen[v.StoryID] {
				continue
			}
			seen[v.StoryID] = true
			distinct = append(distinct, v)
		}

		deduped = append(deduped, distinct)
	}

	return deduped
}
