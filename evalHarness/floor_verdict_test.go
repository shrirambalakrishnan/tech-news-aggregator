package evalHarness

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/shrirambalakrishnan/tech-news/rag"
)

func TestAreaUnderROC(t *testing.T) {
	cases := []struct {
		name     string
		pos, neg []float64
		want     float64
	}{
		// Every positive outranks every negative.
		{"perfect separation", []float64{0.8, 0.9}, []float64{0.1, 0.2}, 1.0},
		// Every positive is outranked: perfectly wrong, which is still perfectly
		// informative - hence 0, not 0.5.
		{"inverted", []float64{0.1, 0.2}, []float64{0.8, 0.9}, 0.0},
		// 4 pairs: 0.5>0.4 and 0.5>0.2 win, 0.3<0.4 loses, 0.3>0.2 wins -> 3/4.
		{"partial overlap", []float64{0.5, 0.3}, []float64{0.4, 0.2}, 0.75},
		// Identical distributions: every pair is a tie, counting half each.
		{"all ties", []float64{0.5, 0.5}, []float64{0.5, 0.5}, 0.5},
	}
	for _, c := range cases {
		if got := areaUnderROC(c.pos, c.neg); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: AUC = %v, want %v", c.name, got, c.want)
		}
	}

	if got := areaUnderROC(nil, []float64{0.1}); got != 0 {
		t.Errorf("AUC with no positives = %v, want 0", got)
	}
}

func TestDPrime(t *testing.T) {
	// Means 0.1 apart, both groups with population stddev 0.05 -> d' = 2.
	pos := []float64{0.55, 0.45}
	neg := []float64{0.45, 0.35}
	if got := dPrime(pos, neg); math.Abs(got-2.0) > 1e-9 {
		t.Errorf("d' = %v, want 2", got)
	}

	// Identical groups carry no signal, and zero spread must not divide by zero.
	same := []float64{0.5, 0.5}
	if got := dPrime(same, same); got != 0 {
		t.Errorf("d' of identical constant groups = %v, want 0", got)
	}
}

func TestBestFloorByYoudenJ(t *testing.T) {
	t.Run("finds the gap between separated groups", func(t *testing.T) {
		pos := []float64{0.7, 0.8, 0.9}
		neg := []float64{0.1, 0.2, 0.3}

		got := bestFloorByYoudenJ(pos, neg)
		// 0.7 is the lowest floor keeping all positives and no negatives.
		if math.Abs(got.Floor-0.7) > 1e-9 {
			t.Errorf("floor = %v, want 0.7", got.Floor)
		}
		if got.TPR != 1.0 || got.FPR != 0.0 || math.Abs(got.J-1.0) > 1e-9 {
			t.Errorf("TPR/FPR/J = %v/%v/%v, want 1/0/1", got.TPR, got.FPR, got.J)
		}
	})

	t.Run("reports a poor best when the groups overlap", func(t *testing.T) {
		// Fully interleaved: no floor separates them, so J stays small.
		pos := []float64{0.2, 0.4, 0.6}
		neg := []float64{0.1, 0.3, 0.5}

		got := bestFloorByYoudenJ(pos, neg)
		if got.J > 0.4 {
			t.Errorf("J = %v, want a small gap for interleaved groups", got.J)
		}
	})

	t.Run("finds an optimum an evenly spaced grid would step over", func(t *testing.T) {
		// The optimum is at 0.51, which candidateFloors' even spacing across
		// [0, 1] never lands on. Sweeping observed scores does.
		pos := []float64{0.51, 0.52}
		neg := []float64{0.49, 0.50}

		if got := bestFloorByYoudenJ(pos, neg); math.Abs(got.Floor-0.51) > 1e-9 {
			t.Errorf("floor = %v, want 0.51", got.Floor)
		}
	})
}

func TestSimulatePoolCap(t *testing.T) {
	t.Run("implied floor is the score of the last surviving chunk", func(t *testing.T) {
		// 4 stories x 2 chunks = 8 candidates, cap 3 -> 0.9/0.8/0.7 survive and
		// the floor is the weakest of those, 0.7.
		perStory := [][]float64{{0.9, 0.8}, {0.7, 0.6}, {0.5, 0.4}, {0.3, 0.2}}

		got := simulatePoolCap(perStory, 4, 3)
		if got.Batches != 1 || got.BindingCount != 1 {
			t.Fatalf("batches/binding = %d/%d, want 1/1", got.Batches, got.BindingCount)
		}
		if math.Abs(got.MedianImplied-0.7) > 1e-9 {
			t.Errorf("implied floor = %v, want 0.7 (the last chunk to survive)", got.MedianImplied)
		}
		if !got.Binds() {
			t.Error("Binds() = false, want true when truncation fires")
		}
	})

	t.Run("cap imposes nothing when the pool is smaller than it", func(t *testing.T) {
		perStory := [][]float64{{0.9}, {0.7}}

		got := simulatePoolCap(perStory, 2, 20)
		if got.BindingCount != 0 || len(got.ImpliedFloors) != 0 {
			t.Errorf("binding = %d, floors = %v, want none", got.BindingCount, got.ImpliedFloors)
		}
		if got.Binds() {
			t.Error("Binds() = true, want false when the cap never fires")
		}
	})

	t.Run("splits into batches the way the eval does", func(t *testing.T) {
		perStory := [][]float64{{0.9}, {0.8}, {0.7}, {0.6}, {0.5}}

		got := simulatePoolCap(perStory, 2, 1)
		// Batches of 2 over 5 stories -> 3 batches. The first two hold 2 chunks
		// each and truncate to their top score; the trailing 1-story batch is
		// already at the cap, so it contributes no implied floor.
		if got.Batches != 3 || got.BindingCount != 2 {
			t.Fatalf("batches/binding = %d/%d, want 3/2", got.Batches, got.BindingCount)
		}
		want := []float64{0.9, 0.7}
		if len(got.ImpliedFloors) != len(want) {
			t.Fatalf("implied floors = %v, want %v", got.ImpliedFloors, want)
		}
		for i := range want {
			if math.Abs(got.ImpliedFloors[i]-want[i]) > 1e-9 {
				t.Fatalf("implied floors = %v, want %v", got.ImpliedFloors, want)
			}
		}
	})

	t.Run("no stories yields an empty simulation", func(t *testing.T) {
		if got := simulatePoolCap(nil, 30, 20); got.Batches != 0 {
			t.Errorf("batches = %d, want 0", got.Batches)
		}
	})
}

func TestMedianOf(t *testing.T) {
	if got := medianOf([]float64{0.3, 0.1, 0.2}); math.Abs(got-0.2) > 1e-9 {
		t.Errorf("odd-length median = %v, want 0.2", got)
	}
	if got := medianOf([]float64{0.4, 0.1, 0.2, 0.3}); math.Abs(got-0.25) > 1e-9 {
		t.Errorf("even-length median = %v, want 0.25", got)
	}
	if got := medianOf(nil); got != 0 {
		t.Errorf("median of nothing = %v, want 0", got)
	}
}

func TestDecideFloor(t *testing.T) {
	t.Run("recommends the best floor when it separates and fires", func(t *testing.T) {
		best := floorCandidate{Floor: 0.7, TPR: 0.95, FPR: 0.05, J: 0.90}
		pool := poolCapSimulation{Batches: 2, BindingCount: 2, MedianImplied: 0.3}

		floor, reason := decideFloor(best, pool)
		if math.Abs(floor-0.7) > 1e-9 {
			t.Errorf("floor = %v, want 0.7", floor)
		}
		if !strings.HasPrefix(reason, "USABLE") {
			t.Errorf("reason = %q, want a USABLE verdict", reason)
		}
	})

	t.Run("calls a floor inert when the pool cap already enforces more", func(t *testing.T) {
		// A strong J must NOT rescue a floor the cap has already applied: it
		// would change nothing, and reporting it as effective would be wrong.
		best := floorCandidate{Floor: 0.23, TPR: 0.97, FPR: 0.05, J: 0.92}
		pool := poolCapSimulation{Batches: 2, BindingCount: 2, MedianImplied: 0.32}

		floor, reason := decideFloor(best, pool)
		if floor != 0.0 {
			t.Errorf("floor = %v, want 0 for an inert floor", floor)
		}
		if !strings.HasPrefix(reason, "INERT") {
			t.Errorf("reason = %q, want an INERT verdict", reason)
		}
	})

	t.Run("calls a floor unusable when the groups overlap", func(t *testing.T) {
		best := floorCandidate{Floor: 0.5, TPR: 0.60, FPR: 0.45, J: 0.15}
		pool := poolCapSimulation{Batches: 2, BindingCount: 0} // cap never fires

		floor, reason := decideFloor(best, pool)
		if floor != 0.0 {
			t.Errorf("floor = %v, want 0 for an unusable floor", floor)
		}
		if !strings.HasPrefix(reason, "UNUSABLE") {
			t.Errorf("reason = %q, want an UNUSABLE verdict", reason)
		}
	})
}

func TestLabelFor(t *testing.T) {
	cases := []struct {
		value float64
		want  string
	}{
		{0.95, "STRONG"},
		{0.80, "USABLE"}, // exactly on a band boundary belongs to that band
		{0.66, "WEAK"},
		{0.50, "NONE"},
	}
	for _, c := range cases {
		if got := labelFor(AUC_BANDS, c.value); got != c.want {
			t.Errorf("labelFor(AUC, %v) = %q, want %q", c.value, got, c.want)
		}
	}
}

func TestBuildFloorVerdictReproducesTheRealRun(t *testing.T) {
	// A miniature stand-in for the observed corpus: relevant and irrelevant
	// scores overlapping heavily, which is what the first real calibration run
	// found. The verdict must come out INERT rather than recommending a floor.
	scored := []scoredTitle{
		{story: LabelledStory{Title: "r1", Label: 1}, score: 0.40},
		{story: LabelledStory{Title: "r2", Label: 1}, score: 0.30},
		{story: LabelledStory{Title: "n1", Label: 0}, score: 0.38},
		{story: LabelledStory{Title: "n2", Label: 0}, score: 0.28},
	}
	perStoryTop := [][]float64{{0.40, 0.39}, {0.30, 0.29}, {0.38, 0.37}, {0.28, 0.27}}

	originalBatch, originalCap := EVAL_BATCH_SIZE, rag.RETRIEVAL_POOL_CAP
	EVAL_BATCH_SIZE, rag.RETRIEVAL_POOL_CAP = 4, 2
	defer func() { EVAL_BATCH_SIZE, rag.RETRIEVAL_POOL_CAP = originalBatch, originalCap }()

	got := buildFloorVerdict(scored, perStoryTop)

	// 8 candidates truncated to 2 -> the implied floor is the 2nd best, 0.39,
	// above every candidate floor the scores offer.
	if math.Abs(got.Pool.MedianImplied-0.39) > 1e-9 {
		t.Errorf("implied floor = %v, want 0.39", got.Pool.MedianImplied)
	}
	if got.Recommended != 0.0 {
		t.Errorf("recommended = %v, want 0 when the cap already outranks the best floor", got.Recommended)
	}
	if !strings.HasPrefix(got.Reason, "INERT") {
		t.Errorf("reason = %q, want INERT", got.Reason)
	}
}

func TestWriteVerdict(t *testing.T) {
	verdict := floorVerdict{
		AUC:         0.659,
		DPrime:      0.635,
		Best:        floorCandidate{Floor: 0.2336, TPR: 0.971, FPR: 0.680, J: 0.291},
		Pool:        poolCapSimulation{Batches: 2, BindingCount: 2, ImpliedFloors: []float64{0.35, 0.30}, MedianImplied: 0.325},
		Recommended: 0.0,
		Reason:      "INERT: ...",
	}

	var buf bytes.Buffer
	writeVerdict(&buf, verdict)
	out := buf.String()

	// The report must carry the decision, not only the statistics: reading the
	// numbers by hand is what this section exists to remove.
	for _, want := range []string{"AUC 0.659", "WEAK", "0.2336", "RECOMMENDATION", "INERT", "NO EFFECT"} {
		if !strings.Contains(out, want) {
			t.Errorf("verdict output missing %q:\n%s", want, out)
		}
	}
}
