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
	topScoresPerQuery  = rag.TopScoresPerQuery
	topSourcesPerQuery = rag.TopSourcesPerQuery
	loadDataset        = loadLabelledData
)

// scoredTitle pairs a labelled story with its best retrieval score — the number
// the floor would be compared against for that story — and the name of the
// corpus file that score came from.
type scoredTitle struct {
	story  LabelledStory
	score  float64
	source string
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
// evidence plus the decision on stderr.
//
// The report has four sections. The distribution table and the survival table
// are the evidence — where the label-1 and label-0 scores sit, and what each
// candidate floor would keep. The top-source table says which corpus files are
// winning those matches, which is how a corpus change is checked for having
// displaced anything. The verdict section (floor_verdict.go) reads the score
// evidence and states the value to set, so the conclusion does not depend on
// whoever is looking at the tables.
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

	// One Voyage request per VOYAGE_MAX_BATCH titles; no Claude call. Top-k
	// rather than just the best score: element 0 is what the floor is compared
	// against, and the full k is what the story would contribute to the pool,
	// which is what the pool-cap simulation replays.
	perStoryTop, err := topScoresPerQuery(titles, rag.RETRIEVAL_TOP_K_PER_STORY)
	if err != nil {
		return fmt.Errorf("calibrate-floor: %w", err)
	}
	if len(perStoryTop) != len(dataset) {
		return fmt.Errorf("calibrate-floor: scored %d titles, expected %d", len(perStoryTop), len(dataset))
	}

	// Which document won, not just by how much. A second (free) Voyage round
	// trip over the same titles: the scores alone cannot say whether a corpus
	// addition displaced the files that were previously winning, which is the
	// mechanism any corpus change is betting on (issue #22).
	bestSources, err := topSourcesPerQuery(titles)
	if err != nil {
		return fmt.Errorf("calibrate-floor: %w", err)
	}
	if len(bestSources) != len(dataset) {
		return fmt.Errorf("calibrate-floor: sourced %d titles, expected %d", len(bestSources), len(dataset))
	}

	scored := make([]scoredTitle, 0, len(dataset))
	for i, story := range dataset {
		if len(perStoryTop[i]) == 0 {
			return fmt.Errorf("calibrate-floor: no chunk scored against %q", story.Title)
		}
		scored = append(scored, scoredTitle{story: story, score: perStoryTop[i][0], source: bestSources[i]})
	}

	if err := writeScoreCSV(csvOut, scored); err != nil {
		return fmt.Errorf("calibrate-floor: failed to write CSV: %w", err)
	}
	writeFloorReport(reportOut, scored)
	writeTopSourceReport(reportOut, scored)
	writeVerdict(reportOut, buildFloorVerdict(scored, perStoryTop))
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

	if err := out.Write([]string{"best_score", "label", "best_source", "title"}); err != nil {
		return err
	}
	for _, s := range ranked {
		row := []string{
			strconv.FormatFloat(s.score, 'f', 4, 64),
			strconv.Itoa(s.story.Label),
			s.source,
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
	fmt.Fprintf(w, "\nA usable floor shows up here as a row where the two columns pull apart.\n")
	fmt.Fprintf(w, "This grid is coarse - the Verdict below sweeps every score and decides.\n\n")
}

// sourceTally counts how often one corpus file is a title's best match, split by
// label. The split is what makes the table readable as more than trivia: a file
// that wins mostly label-0 titles is supplying context for stories the reader
// does not want.
type sourceTally struct {
	Source    string
	Wins      int
	WinsLabel int // of those wins, how many were on relevant (label 1) titles
}

// writeTopSourceReport prints which corpus files win the top-1 match, most
// frequent first. It exists to make "did the new documents displace the old
// winners?" a repeatable check rather than a claim: run calibrate-floor before
// and after a corpus change and compare two tables. Free — the scores it reads
// are already computed.
func writeTopSourceReport(w io.Writer, scored []scoredTitle) {
	fmt.Fprintf(w, "\n=== Top-1 match by corpus file ===\n")
	fmt.Fprintf(w, "Which document wins each title's best match. Compare this table\n")
	fmt.Fprintf(w, "before and after a corpus change to see what got displaced.\n\n")
	fmt.Fprintf(w, "%12s %10s %10s  %s\n", "top-1 wins", "share", "on label 1", "source")

	for _, t := range tallyTopSources(scored) {
		fmt.Fprintf(w, "%12d %9.1f%% %10d  %s\n",
			t.Wins, percentOf(t.Wins, len(scored)), t.WinsLabel, t.Source)
	}
	fmt.Fprintf(w, "\n")
}

// tallyTopSources is pure: it counts top-1 wins per source, ordered by wins
// desc and then by name so equal counts print in a stable order.
func tallyTopSources(scored []scoredTitle) []sourceTally {
	position := map[string]int{}
	tallies := []sourceTally{}

	for _, s := range scored {
		index, seen := position[s.source]
		if !seen {
			index = len(tallies)
			position[s.source] = index
			tallies = append(tallies, sourceTally{Source: s.source})
		}
		tallies[index].Wins++
		if s.story.Label == 1 {
			tallies[index].WinsLabel++
		}
	}

	sort.SliceStable(tallies, func(i, j int) bool {
		if tallies[i].Wins != tallies[j].Wins {
			return tallies[i].Wins > tallies[j].Wins
		}
		return tallies[i].Source < tallies[j].Source
	})
	return tallies
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
