package evalHarness

import (
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/shrirambalakrishnan/tech-news/rag"
)

// This file turns the calibration measurements into a decision. The tables in
// calibrate_floor.go print the evidence; everything here answers the question
// the evidence was gathered for — "what should RETRIEVAL_SIMILARITY_FLOOR be?" —
// and prints that answer in the report.
//
// It was added after the first real calibration run, where the tables alone were
// not enough to decide: the numbers had to be re-analysed by hand afterwards to
// reach a conclusion. That made the conclusion unreproducible, which defeats the
// point of measuring instead of guessing. The three things that hand analysis
// needed are now computed here: a summary of how much signal exists at all
// (AUC/d'), the best floor found on a fine sweep rather than a 10-row grid, and
// the interaction with RETRIEVAL_POOL_CAP.
//
// The pool-cap part is the one that changes the answer. The floor is not the
// only gate on chunks: pooling truncates to RETRIEVAL_POOL_CAP, which is itself
// a threshold — a relative one, at whatever score the last surviving chunk
// happens to have. Any floor below that score filters nothing the cap did not
// already filter. Measuring the floor in isolation therefore describes a gate
// that may never fire, and "weak floor" and "inert floor" call for different
// next moves.

// Benchmarks for turning the raw statistics into words. They are conventional
// reading points, not thresholds with theory behind them; they exist so the
// report interprets its own numbers rather than assuming the reader knows what
// counts as a good AUC.
var (
	// AUC_BANDS maps a lower bound on AUC to its label, strongest first.
	AUC_BANDS = []scoreBand{
		{0.90, "STRONG"},
		{0.80, "USABLE"},
		{0.65, "WEAK"},
		{0.00, "NONE"},
	}

	// D_PRIME_BANDS maps a lower bound on d' to its label, strongest first.
	D_PRIME_BANDS = []scoreBand{
		{2.00, "STRONG"},
		{1.00, "MODERATE"},
		{0.50, "WEAK"},
		{0.00, "NONE"},
	}

	// YOUDEN_J_BANDS maps a lower bound on Youden's J to its label. J is the
	// percentage-point gap between relevant kept and irrelevant kept, so it is
	// the direct measure of what a floor buys.
	YOUDEN_J_BANDS = []scoreBand{
		{0.60, "USABLE"},
		{0.40, "MARGINAL"},
		{0.25, "WEAK"},
		{0.00, "UNUSABLE"},
	}

	// FLOOR_MIN_USABLE_J is the smallest Youden's J worth setting a floor for.
	// Below it the floor discards real positives about as fast as noise, which
	// is a bad trade on a dataset with 35 positives.
	FLOOR_MIN_USABLE_J = 0.25
)

// scoreBand labels every value at or above Min.
type scoreBand struct {
	Min   float64
	Label string
}

func labelFor(bands []scoreBand, value float64) string {
	for _, b := range bands {
		if value >= b.Min {
			return b.Label
		}
	}
	return bands[len(bands)-1].Label
}

// floorCandidate is one operating point: set the floor here, and these are the
// two rates that follow.
type floorCandidate struct {
	Floor float64
	// TPR (true positive rate) is the fraction of relevant stories that keep
	// their corpus support. FPR (false positive rate) is the same for
	// irrelevant stories. J is TPR-FPR: how much noise the floor removes per
	// unit of signal it costs.
	TPR, FPR, J float64
}

// poolCapSimulation is what RETRIEVAL_POOL_CAP does on its own, with no floor
// set. ImpliedFloors holds the score of the last chunk to survive truncation in
// each batch — the floor the cap is already enforcing there.
type poolCapSimulation struct {
	Batches       int
	BindingCount  int // batches where the pool exceeded the cap
	ImpliedFloors []float64
	MedianImplied float64
}

// Binds reports whether truncation actually removed chunks in most batches. If
// it does not, the cap is not competing with the floor and the floor is free to
// act on its own.
func (p poolCapSimulation) Binds() bool {
	return p.Batches > 0 && p.BindingCount*2 > p.Batches
}

// floorVerdict is the whole recommendation: the evidence, the best operating
// point, and the value to actually set.
type floorVerdict struct {
	AUC, DPrime float64
	Best        floorCandidate
	Pool        poolCapSimulation
	Recommended float64
	Reason      string
}

// areaUnderROC is the probability that a randomly chosen relevant story scores
// higher than a randomly chosen irrelevant one, ties counting half. 0.5 means
// the score carries no information; 1.0 means perfect ranking. Computed by
// direct pair counting rather than by ranking: the dataset is a few hundred
// rows, so the O(n*m) cost is irrelevant and the definition stays visible.
func areaUnderROC(positives, negatives []float64) float64 {
	if len(positives) == 0 || len(negatives) == 0 {
		return 0
	}
	wins := 0.0
	for _, p := range positives {
		for _, n := range negatives {
			switch {
			case p > n:
				wins++
			case p == n:
				wins += 0.5
			}
		}
	}
	return wins / float64(len(positives)*len(negatives))
}

// dPrime is the distance between the two groups' means measured in units of
// their own spread: (meanPos-meanNeg) / rootMeanSquare(stddevs). It answers "are
// these two distributions far apart relative to how wide they are", which is the
// question a threshold depends on — two groups 0.05 apart separate cleanly if
// each is 0.01 wide and not at all if each is 0.08 wide.
func dPrime(positives, negatives []float64) float64 {
	if len(positives) == 0 || len(negatives) == 0 {
		return 0
	}
	meanPos, sdPos := meanAndStdDev(positives)
	meanNeg, sdNeg := meanAndStdDev(negatives)

	spread := math.Sqrt((sdPos*sdPos + sdNeg*sdNeg) / 2)
	if spread == 0 {
		return 0
	}
	return (meanPos - meanNeg) / spread
}

// meanAndStdDev returns the mean and the population standard deviation.
func meanAndStdDev(values []float64) (float64, float64) {
	if len(values) == 0 {
		return 0, 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	mean := sum / float64(len(values))

	variance := 0.0
	for _, v := range values {
		variance += (v - mean) * (v - mean)
	}
	return mean, math.Sqrt(variance / float64(len(values)))
}

// bestFloorByYoudenJ sweeps every observed score as a candidate floor and keeps
// the one with the largest TPR-FPR gap. Every score, not an evenly spaced grid:
// the optimum sits at some actually-observed value, and a coarse grid can step
// straight over it — the first calibration run's 10-row table missed its own
// optimum by landing either side of it.
func bestFloorByYoudenJ(positives, negatives []float64) floorCandidate {
	if len(positives) == 0 || len(negatives) == 0 {
		return floorCandidate{}
	}

	candidates := append(append([]float64{}, positives...), negatives...)
	sort.Float64s(candidates)

	best := floorCandidate{J: -math.MaxFloat64}
	for _, floor := range candidates {
		tpr := float64(countAtOrAbove(positives, floor)) / float64(len(positives))
		fpr := float64(countAtOrAbove(negatives, floor)) / float64(len(negatives))
		if j := tpr - fpr; j > best.J {
			best = floorCandidate{Floor: floor, TPR: tpr, FPR: fpr, J: j}
		}
	}
	return best
}

// simulatePoolCap replays the pooling arithmetic arm 3 performs, to find the
// threshold RETRIEVAL_POOL_CAP already imposes. Per batch of batchSize stories,
// each story contributes its top-k chunk scores; the pool is sorted and cut to
// poolCap, so the score at position poolCap is the implied floor.
//
// One simplification: real pooling de-duplicates chunks retrieved by several
// stories, which this cannot do from scores alone. Dedupe only ever shrinks the
// pool, so it can only make the cap bind less often — the implied floor reported
// here is therefore an upper bound, and the BindingCount is a best case.
func simulatePoolCap(perStoryTopScores [][]float64, batchSize, poolCap int) poolCapSimulation {
	if len(perStoryTopScores) == 0 || batchSize < 1 || poolCap < 1 {
		return poolCapSimulation{}
	}

	sim := poolCapSimulation{}
	for start := 0; start < len(perStoryTopScores); start += batchSize {
		end := min(start+batchSize, len(perStoryTopScores))

		pool := []float64{}
		for _, scores := range perStoryTopScores[start:end] {
			pool = append(pool, scores...)
		}
		sort.Sort(sort.Reverse(sort.Float64Slice(pool)))

		sim.Batches++
		if len(pool) <= poolCap {
			continue // the cap never fires here, so it imposes no floor
		}
		sim.BindingCount++
		sim.ImpliedFloors = append(sim.ImpliedFloors, pool[poolCap-1])
	}

	sim.MedianImplied = medianOf(sim.ImpliedFloors)
	return sim
}

func medianOf(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64{}, values...)
	sort.Float64s(sorted)

	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// decideFloor is the recommendation itself, kept apart from the printing so the
// rule is testable and readable as a rule. Three outcomes, checked in order:
//
//  1. INERT — the best floor sits below what the pool cap already enforces, so
//     setting it would change nothing. Ship 0 and record that the floor was
//     never in effect, which is a different finding from "it did not help".
//  2. UNUSABLE — the floor would fire, but it discards relevant stories nearly
//     as fast as irrelevant ones. Ship 0 and spend the effort on retrieval
//     quality (chunk size, corpus coverage, reranking) instead.
//  3. Otherwise the floor is worth setting, at the best operating point found.
func decideFloor(best floorCandidate, pool poolCapSimulation) (float64, string) {
	switch {
	case pool.Binds() && best.Floor <= pool.MedianImplied:
		return 0.0, fmt.Sprintf(
			"INERT: the best floor (%.4f) is below the ~%.4f that RETRIEVAL_POOL_CAP=%d already enforces, so it would filter nothing new. Any floor high enough to fire cuts into relevant stories.",
			best.Floor, pool.MedianImplied, rag.RETRIEVAL_POOL_CAP)
	case best.J < FLOOR_MIN_USABLE_J:
		return 0.0, fmt.Sprintf(
			"UNUSABLE: the best gap between relevant kept and irrelevant kept is only %.1f pts (need >%.0f). The two score distributions overlap; no threshold separates them.",
			best.J*100, FLOOR_MIN_USABLE_J*100)
	default:
		return best.Floor, fmt.Sprintf(
			"USABLE: keeps %.1f%% of relevant stories while dropping %.1f%% of irrelevant ones (gap %.1f pts).",
			best.TPR*100, (1-best.FPR)*100, best.J*100)
	}
}

// buildFloorVerdict runs every measurement over the scored dataset. perStoryTop
// carries each story's top-k chunk scores in dataset order, which the pool-cap
// simulation needs and the label-split statistics do not.
func buildFloorVerdict(scored []scoredTitle, perStoryTop [][]float64) floorVerdict {
	positives := scoresForLabel(scored, 1)
	negatives := scoresForLabel(scored, 0)

	verdict := floorVerdict{
		AUC:    areaUnderROC(positives, negatives),
		DPrime: dPrime(positives, negatives),
		Best:   bestFloorByYoudenJ(positives, negatives),
		Pool:   simulatePoolCap(perStoryTop, EVAL_BATCH_SIZE, rag.RETRIEVAL_POOL_CAP),
	}
	verdict.Recommended, verdict.Reason = decideFloor(verdict.Best, verdict.Pool)
	return verdict
}

// writeVerdict prints the decision section: how much signal exists, the best
// operating point, what the pool cap is already doing, and the value to set.
func writeVerdict(w io.Writer, v floorVerdict) {
	fmt.Fprintf(w, "=== Verdict ===\n")

	fmt.Fprintf(w, "Signal strength (is there anything to threshold on?):\n")
	fmt.Fprintf(w, "  AUC %.3f   (0.50 = coin flip, 0.80 = usable, 1.00 = perfect)   -> %s\n",
		v.AUC, labelFor(AUC_BANDS, v.AUC))
	fmt.Fprintf(w, "  d'  %.3f   (0.0 = none, 1.0 = moderate, 2.0 = strong)          -> %s\n\n",
		v.DPrime, labelFor(D_PRIME_BANDS, v.DPrime))

	fmt.Fprintf(w, "Best floor (fine sweep over every observed score, not the table above):\n")
	fmt.Fprintf(w, "  floor %.4f  keeps %.1f%% relevant / %.1f%% irrelevant  (gap %.1f pts)\n",
		v.Best.Floor, v.Best.TPR*100, v.Best.FPR*100, v.Best.J*100)
	fmt.Fprintf(w, "  gap benchmark: >%.0f usable, %.0f-%.0f weak, <%.0f unusable   -> %s\n\n",
		YOUDEN_J_BANDS[0].Min*100, YOUDEN_J_BANDS[2].Min*100, YOUDEN_J_BANDS[1].Min*100,
		YOUDEN_J_BANDS[2].Min*100, labelFor(YOUDEN_J_BANDS, v.Best.J))

	writePoolCapSection(w, v.Pool)

	fmt.Fprintf(w, "RECOMMENDATION: set rag.RETRIEVAL_SIMILARITY_FLOOR = %.4f\n", v.Recommended)
	fmt.Fprintf(w, "  %s\n", v.Reason)
	fmt.Fprintf(w, "  Re-run this after changing the corpus, the chunk size, or RETRIEVAL_POOL_CAP.\n")
	fmt.Fprintf(w, "  The floor is fit on the labelled set, so the arm's score will be optimistic.\n\n")
}

func writePoolCapSection(w io.Writer, pool poolCapSimulation) {
	fmt.Fprintf(w, "Interaction with RETRIEVAL_POOL_CAP = %d (top-%d per story, %d per batch):\n",
		rag.RETRIEVAL_POOL_CAP, rag.RETRIEVAL_TOP_K_PER_STORY, EVAL_BATCH_SIZE)

	if !pool.Binds() {
		fmt.Fprintf(w, "  Truncation fires in only %d of %d batches, so the cap imposes no\n",
			pool.BindingCount, pool.Batches)
		fmt.Fprintf(w, "  competing threshold and the floor acts on its own.\n\n")
		return
	}

	fmt.Fprintf(w, "  The cap discards everything below the %d-th ranked chunk, which is\n", rag.RETRIEVAL_POOL_CAP)
	fmt.Fprintf(w, "  itself a floor. Simulated over this dataset (%d of %d batches truncate):\n",
		pool.BindingCount, pool.Batches)
	fmt.Fprintf(w, "  implied floor per batch:")
	for _, f := range pool.ImpliedFloors {
		fmt.Fprintf(w, " %.3f", f)
	}
	fmt.Fprintf(w, "\n  median %.4f -> any floor below this has NO EFFECT.\n", pool.MedianImplied)
	fmt.Fprintf(w, "  (Dedupe can only shrink the pool, so this is an upper bound.)\n\n")
}
