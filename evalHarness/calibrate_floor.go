package evalHarness

import (
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"sort"
	"strconv"

	"github.com/shrirambalakrishnan/tech-news/rag"
)

// This file calibrates ONE parameter: rag.RETRIEVAL_SIMILARITY_FLOOR, the
// cosine-similarity threshold below which a story is treated as having no
// corpus support. It measures the score distribution over the labelled dataset
// so that threshold is chosen from evidence instead of guessed. It classifies
// nothing — the only quantity of interest is cosine similarity, so a Claude call
// here would pay to generate predictions that get thrown away. Cost is one
// Voyage request per 128 titles and nothing else.
//
// Why not just guess the floor: cosine values have no meaning independent of the
// model that produced them, and here the two sides are asymmetric (a ~10-word
// title against an 800-word chunk), so intuition from other projects doesn't
// transfer. A guessed floor that sits below the whole observed range never fires
// — and then "the floor didn't help" is indistinguishable from "the floor was
// never in effect". Measuring buys attributability, not accuracy. Worked example
// of reading the floor off the labelled scores: issue #19.
//
// It is permanent, not a throwaway: the right floor is a property of *this*
// corpus, so it has to be re-derived every time `go run . embed` runs over new
// documents — the same lifecycle as `prebuild`.
//
// ⚠️ Eval hygiene: calibrating on the labelled set fits the floor to the test
// set, so the arm's reported numbers are optimistic by an unknown amount. With
// 35 positives a proper holdout isn't practical; the honest move is to record
// the caveat next to the result. If the arm wins only narrowly, suspect this
// first.

// CALIBRATION_FLOOR_STEPS is how many candidate floors the survival table
// sweeps across the observed score range. 10 steps is enough to see where the
// two label distributions separate without burying the table.
var CALIBRATION_FLOOR_STEPS = 10

// DI seams (function-variable convention): tests swap these to calibrate
// without touching the network or the git-ignored dataset.
var (
	bestSimilarityPerQuery = rag.BestSimilarityPerQuery
	loadDataset            = loadLabelledData
)

// scoredTitle pairs a labelled story with its best retrieval score — the number
// the floor would be compared against for that story.
type scoredTitle struct {
	story LabelledStory
	score float64
}

// scoreSummary describes one label's score distribution. Percentiles rather than
// mean/stddev because the decision is "where do the two distributions separate",
// which is a question about order statistics, not about central tendency.
type scoreSummary struct {
	Count              int
	Min, P10, P25, P50 float64
	P75, P90, Max      float64
}

// RunFloorCalibration is the `go run . calibrate-floor` entrypoint: score every
// labelled title against the corpus index, print the per-title scores as CSV on
// stdout (so it can be redirected to a file for plotting), and print the
// distribution summary plus a floor-survival table on stderr.
//
// The floor is then read off the point where the label-1 and label-0
// distributions separate — i.e. where "this story has real corpus support" stops
// meaning "these are merely the least-irrelevant chunks".
func RunFloorCalibration() error {
	return calibrateFloor(os.Stdout, os.Stderr)
}

// calibrateFloor is RunFloorCalibration with its two output streams injected, so
// tests can exercise the whole flow without writing to the console.
func calibrateFloor(csvOut, reportOut io.Writer) error {
	dataset, err := loadDataset(LABELLED_DATA_FILE)
	if err != nil {
		return fmt.Errorf("calibrate-floor: failed to load labelled data: %w", err)
	}
	log.Printf("calibrate-floor: loaded %d labelled stories from %s", len(dataset), LABELLED_DATA_FILE)

	titles := make([]string, 0, len(dataset))
	for _, story := range dataset {
		titles = append(titles, story.Title)
	}

	// One Voyage request per VOYAGE_MAX_BATCH titles; no Claude call.
	scores, err := bestSimilarityPerQuery(titles)
	if err != nil {
		return fmt.Errorf("calibrate-floor: %w", err)
	}
	if len(scores) != len(dataset) {
		return fmt.Errorf("calibrate-floor: scored %d titles, expected %d", len(scores), len(dataset))
	}

	scored := make([]scoredTitle, 0, len(dataset))
	for i, story := range dataset {
		scored = append(scored, scoredTitle{story: story, score: scores[i]})
	}

	if err := writeScoreCSV(csvOut, scored); err != nil {
		return fmt.Errorf("calibrate-floor: failed to write CSV: %w", err)
	}
	writeFloorReport(reportOut, scored)
	return nil
}

// writeScoreCSV emits one row per title, best-scoring first so the head of the
// file is the most-supported stories. CSV (not a hand-rolled format) because
// titles contain commas and quotes.
func writeScoreCSV(w io.Writer, scored []scoredTitle) error {
	ranked := append([]scoredTitle{}, scored...)
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	out := csv.NewWriter(w)
	defer out.Flush()

	if err := out.Write([]string{"best_score", "label", "title"}); err != nil {
		return err
	}
	for _, s := range ranked {
		row := []string{
			strconv.FormatFloat(s.score, 'f', 4, 64),
			strconv.Itoa(s.story.Label),
			s.story.Title,
		}
		if err := out.Write(row); err != nil {
			return err
		}
	}
	out.Flush()
	return out.Error()
}

// writeFloorReport prints the two things the floor is chosen from: how the
// relevant and irrelevant stories' scores are distributed, and how many of each
// survive at candidate floors.
func writeFloorReport(w io.Writer, scored []scoredTitle) {
	positives := scoresForLabel(scored, 1)
	negatives := scoresForLabel(scored, 0)

	fmt.Fprintf(w, "\n=== Best-score distribution by label ===\n")
	fmt.Fprintf(w, "%-18s %5s %8s %8s %8s %8s %8s %8s %8s\n",
		"label", "n", "min", "p10", "p25", "p50", "p75", "p90", "max")
	writeSummaryRow(w, "1 (relevant)", summarizeScores(positives))
	writeSummaryRow(w, "0 (not relevant)", summarizeScores(negatives))

	fmt.Fprintf(w, "\n=== Survival at candidate floors ===\n")
	fmt.Fprintf(w, "A story survives when its best chunk scores >= the floor;\n")
	fmt.Fprintf(w, "pick a floor that keeps most label-1 stories while dropping label-0 ones.\n\n")
	fmt.Fprintf(w, "%8s %22s %22s\n", "floor", "label 1 kept", "label 0 kept")

	allScores := make([]float64, 0, len(scored))
	for _, s := range scored {
		allScores = append(allScores, s.score)
	}
	for _, floor := range candidateFloors(allScores, CALIBRATION_FLOOR_STEPS) {
		keptPositive := countAtOrAbove(positives, floor)
		keptNegative := countAtOrAbove(negatives, floor)
		fmt.Fprintf(w, "%8.4f %14d (%5.1f%%) %14d (%5.1f%%)\n",
			floor,
			keptPositive, percentOf(keptPositive, len(positives)),
			keptNegative, percentOf(keptNegative, len(negatives)),
		)
	}
	fmt.Fprintf(w, "\nSet rag.RETRIEVAL_SIMILARITY_FLOOR from the row where the two columns separate.\n")
	fmt.Fprintf(w, "Note: this floor is fit on the labelled set, so the arm's score will be optimistic.\n\n")
}

func writeSummaryRow(w io.Writer, label string, s scoreSummary) {
	fmt.Fprintf(w, "%-18s %5d %8.4f %8.4f %8.4f %8.4f %8.4f %8.4f %8.4f\n",
		label, s.Count, s.Min, s.P10, s.P25, s.P50, s.P75, s.P90, s.Max)
}

// scoresForLabel pulls out the scores of stories carrying the given label.
func scoresForLabel(scored []scoredTitle, label int) []float64 {
	out := []float64{}
	for _, s := range scored {
		if s.story.Label == label {
			out = append(out, s.score)
		}
	}
	return out
}

// summarizeScores is pure: it sorts a copy and reads off the order statistics.
func summarizeScores(scores []float64) scoreSummary {
	if len(scores) == 0 {
		return scoreSummary{}
	}
	sorted := append([]float64{}, scores...)
	sort.Float64s(sorted)

	return scoreSummary{
		Count: len(sorted),
		Min:   sorted[0],
		P10:   percentile(sorted, 10),
		P25:   percentile(sorted, 25),
		P50:   percentile(sorted, 50),
		P75:   percentile(sorted, 75),
		P90:   percentile(sorted, 90),
		Max:   sorted[len(sorted)-1],
	}
}

// percentile returns the nearest-rank percentile of an ascending-sorted slice:
// the smallest value at or above p% of the data. Nearest-rank (no interpolation)
// because every value returned is an actually-observed score, which is what you
// want when the number becomes a threshold.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// candidateFloors returns evenly spaced thresholds spanning the observed range,
// inclusive of both ends. Spanning the *observed* range rather than a fixed
// 0..1 sweep is the point: if this embedding model clusters everything in
// 0.7-0.9, a 0..1 sweep would spend most of its rows on empty space and hide
// the separation.
func candidateFloors(scores []float64, steps int) []float64 {
	if len(scores) == 0 || steps < 1 {
		return nil
	}
	low, high := scores[0], scores[0]
	for _, s := range scores {
		low = math.Min(low, s)
		high = math.Max(high, s)
	}
	if low == high {
		return []float64{low}
	}

	floors := make([]float64, 0, steps+1)
	width := (high - low) / float64(steps)
	for i := 0; i <= steps; i++ {
		floors = append(floors, low+width*float64(i))
	}
	return floors
}

// countAtOrAbove counts scores that clear the floor — the same >= comparison
// topKAboveFloor applies, so the table predicts what retrieval will actually do.
func countAtOrAbove(scores []float64, floor float64) int {
	n := 0
	for _, s := range scores {
		if s >= floor {
			n++
		}
	}
	return n
}

func percentOf(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}
