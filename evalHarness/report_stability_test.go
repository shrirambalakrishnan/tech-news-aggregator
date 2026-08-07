package evalHarness

import "testing"

// verdicts builds one run's rows from {storyID: flagged} plus a label lookup,
// so a fixture reads as "which stories this run flagged" and nothing else.
func verdicts(labels map[int]int, flagged ...int) []storyVerdict {
	isFlagged := map[int]bool{}
	for _, id := range flagged {
		isFlagged[id] = true
	}

	rows := []storyVerdict{}
	for id, label := range labels {
		rows = append(rows, storyVerdict{StoryID: id, Label: label, Predicted: isFlagged[id]})
	}
	return rows
}

func assertBuckets(t *testing.T, label string, got stabilityBuckets, never, sometimes, always int) {
	t.Helper()
	want := stabilityBuckets{Never: never, Sometimes: sometimes, Always: always}
	if got != want {
		t.Errorf("%s = %+v, want %+v", label, got, want)
	}
}

func TestBucketStabilitySplitsOnHowManyRunsFlaggedEachStory(t *testing.T) {
	labels := map[int]int{
		1: 1, // relevant, flagged by both runs
		2: 1, // relevant, flagged by one run
		3: 1, // relevant, flagged by neither
		4: 0, // irrelevant, flagged by both - a stable false alarm
		5: 0, // irrelevant, flagged by one - a flaky false alarm
		6: 0, // irrelevant, never flagged
	}

	table := bucketStability([][]storyVerdict{
		verdicts(labels, 1, 2, 4, 5),
		verdicts(labels, 1, 4),
	})

	if table.N != 2 {
		t.Errorf("n = %d, want 2", table.N)
	}
	assertBuckets(t, "relevant", table.Relevant, 1, 1, 1)
	assertBuckets(t, "irrelevant", table.Irrelevant, 1, 1, 1)
}

// "always" means every run, not "most runs" - at n=3 a story two runs agreed on
// is still a story the runs disagreed about.
func TestBucketStabilityAlwaysMeansEveryRun(t *testing.T) {
	labels := map[int]int{1: 1, 2: 1}

	table := bucketStability([][]storyVerdict{
		verdicts(labels, 1, 2),
		verdicts(labels, 1, 2),
		verdicts(labels, 1),
	})

	if table.N != 3 {
		t.Errorf("n = %d, want 3", table.N)
	}
	assertBuckets(t, "relevant", table.Relevant, 0, 1, 1)
}

// The dataset has 341 rows but 331 distinct story ids. A duplicated id is one
// story: counting it twice would let its flag count exceed n, and it would never
// be bucketed as "always".
func TestBucketStabilityCountsADuplicatedStoryIDOnce(t *testing.T) {
	duplicated := []storyVerdict{
		{StoryID: 7, Label: 0, Predicted: true},
		{StoryID: 7, Label: 0, Predicted: true},
	}

	table := bucketStability([][]storyVerdict{duplicated, duplicated})

	assertBuckets(t, "irrelevant", table.Irrelevant, 0, 0, 1)
	if got := table.Irrelevant.Total(); got != 1 {
		t.Errorf("irrelevant total = %d, want 1 distinct story", got)
	}
}

func TestBucketStabilityTotalsAreTheDistinctStoryCounts(t *testing.T) {
	labels := map[int]int{1: 1, 2: 1, 3: 0, 4: 0, 5: 0}

	table := bucketStability([][]storyVerdict{
		verdicts(labels, 1, 3),
		verdicts(labels, 2, 3, 4),
	})

	if got := table.Relevant.Total(); got != 2 {
		t.Errorf("relevant total = %d, want 2", got)
	}
	if got := table.Irrelevant.Total(); got != 3 {
		t.Errorf("irrelevant total = %d, want 3", got)
	}
}

// The two invariants tying the table back to the confusion matrices in Output 2.
// They are what makes the block checkable against numbers the operator already
// has, rather than a set of counts that could be quietly wrong.
func TestBucketStabilityInvariantsAgainstPerRunTP(t *testing.T) {
	// Six relevant stories. Run 1 catches 1,2,3,4 (TP 4); run 2 catches 3,4,5
	// (TP 3). Deliberately NOT nested, so union (5) exceeds the highest per-run
	// TP (4) and the >= in the invariant is doing real work.
	labels := map[int]int{1: 1, 2: 1, 3: 1, 4: 1, 5: 1, 6: 1}
	runTPs := []int{4, 3}

	table := bucketStability([][]storyVerdict{
		verdicts(labels, 1, 2, 3, 4),
		verdicts(labels, 3, 4, 5),
	})

	assertBuckets(t, "relevant", table.Relevant, 1, 3, 2)

	lowest, highest := runTPs[0], runTPs[0]
	for _, tp := range runTPs {
		if tp < lowest {
			lowest = tp
		}
		if tp > highest {
			highest = tp
		}
	}

	if table.Relevant.Always > lowest {
		t.Errorf("always = %d, must be <= the lowest per-run TP (%d)", table.Relevant.Always, lowest)
	}
	union := table.Relevant.Always + table.Relevant.Sometimes
	if union < highest {
		t.Errorf("always+sometimes = %d, must be >= the highest per-run TP (%d)", union, highest)
	}
}

// The shape measured on the two arm-2 runs of 2026-08-06. evalRuns/ is
// git-ignored, so the real files cannot be a test fixture; this reproduces their
// structure - a solid stable core, a few flippers - at a size that can be read.
func TestBucketStabilityReproducesTheArm2Shape(t *testing.T) {
	labels := map[int]int{}
	for id := 1; id <= 6; id++ {
		labels[id] = 1
	}
	for id := 7; id <= 12; id++ {
		labels[id] = 0
	}

	table := bucketStability([][]storyVerdict{
		verdicts(labels, 1, 2, 3, 7, 8, 9),
		verdicts(labels, 1, 2, 4, 7, 8, 10),
	})

	// Relevant: 1,2 both; 3,4 one each; 5,6 neither.
	assertBuckets(t, "relevant", table.Relevant, 2, 2, 2)
	// Irrelevant: 7,8 both; 9,10 one each; 11,12 neither.
	assertBuckets(t, "irrelevant", table.Irrelevant, 2, 2, 2)
}
