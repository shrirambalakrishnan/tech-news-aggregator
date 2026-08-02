package evalHarness

import (
	"bytes"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/shrirambalakrishnan/tech-news/rag"
)

func TestPercentile(t *testing.T) {
	// Nearest-rank on 10 values: rank = ceil(p/100 * n), value = sorted[rank-1].
	sorted := []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0}
	cases := []struct {
		p    float64
		want float64
	}{
		{0, 0.1},   // below rank 1 clamps to the smallest
		{10, 0.1},  // rank 1
		{25, 0.3},  // rank ceil(2.5) = 3
		{50, 0.5},  // rank 5
		{90, 0.9},  // rank 9
		{100, 1.0}, // rank 10
	}
	for _, c := range cases {
		if got := percentile(sorted, c.p); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("percentile(p%v) = %v, want %v", c.p, got, c.want)
		}
	}

	if got := percentile(nil, 50); got != 0 {
		t.Errorf("percentile of no data = %v, want 0", got)
	}
}

func TestSummarizeScores(t *testing.T) {
	scores := []float64{0.5, 0.1, 0.9, 0.3} // deliberately unsorted
	got := summarizeScores(scores)

	if got.Count != 4 {
		t.Errorf("Count = %d, want 4", got.Count)
	}
	if got.Min != 0.1 || got.Max != 0.9 {
		t.Errorf("range = [%v, %v], want [0.1, 0.9]", got.Min, got.Max)
	}
	// n=4: p50 -> rank ceil(2) = 2 -> 0.3; p75 -> rank 3 -> 0.5.
	if got.P50 != 0.3 || got.P75 != 0.5 {
		t.Errorf("p50/p75 = %v/%v, want 0.3/0.5", got.P50, got.P75)
	}
	// The caller's slice must not be reordered underneath it.
	if !reflect.DeepEqual(scores, []float64{0.5, 0.1, 0.9, 0.3}) {
		t.Errorf("summarizeScores mutated its input: %v", scores)
	}

	if empty := summarizeScores(nil); empty.Count != 0 {
		t.Errorf("empty summary Count = %d, want 0", empty.Count)
	}
}

func TestCandidateFloors(t *testing.T) {
	t.Run("spans the observed range inclusively", func(t *testing.T) {
		got := candidateFloors([]float64{0.4, 0.8, 0.6}, 4)
		want := []float64{0.4, 0.5, 0.6, 0.7, 0.8}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if math.Abs(got[i]-want[i]) > 1e-9 {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	})

	t.Run("collapses when every score is identical", func(t *testing.T) {
		if got := candidateFloors([]float64{0.7, 0.7}, 4); !reflect.DeepEqual(got, []float64{0.7}) {
			t.Errorf("got %v, want [0.7]", got)
		}
	})

	t.Run("no scores yields no floors", func(t *testing.T) {
		if got := candidateFloors(nil, 4); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
}

func TestCountAtOrAbove(t *testing.T) {
	scores := []float64{0.2, 0.5, 0.5, 0.9}
	// >= matches topKAboveFloor's comparison: a score exactly at the floor survives.
	if got := countAtOrAbove(scores, 0.5); got != 3 {
		t.Errorf("countAtOrAbove(0.5) = %d, want 3", got)
	}
	if got := countAtOrAbove(scores, 1.0); got != 0 {
		t.Errorf("countAtOrAbove(1.0) = %d, want 0", got)
	}
}

func TestScoresForLabel(t *testing.T) {
	scored := []scoredTitle{
		{story: LabelledStory{Label: 1}, score: 0.9},
		{story: LabelledStory{Label: 0}, score: 0.2},
		{story: LabelledStory{Label: 1}, score: 0.7},
	}
	if got := scoresForLabel(scored, 1); !reflect.DeepEqual(got, []float64{0.9, 0.7}) {
		t.Errorf("label 1 scores = %v, want [0.9 0.7]", got)
	}
	if got := scoresForLabel(scored, 0); !reflect.DeepEqual(got, []float64{0.2}) {
		t.Errorf("label 0 scores = %v, want [0.2]", got)
	}
}

func TestWriteScoreCSV(t *testing.T) {
	scored := []scoredTitle{
		{story: LabelledStory{Title: "low, with comma", Label: 0}, score: 0.1},
		{story: LabelledStory{Title: "high", Label: 1}, score: 0.9},
	}

	var buf bytes.Buffer
	if err := writeScoreCSV(&buf, scored); err != nil {
		t.Fatalf("writeScoreCSV returned error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header + 2 rows, got %q", buf.String())
	}
	if lines[0] != "best_score,label,title" {
		t.Errorf("header = %q", lines[0])
	}
	// Best-scoring first, so the head of the file is the most-supported stories.
	if lines[1] != "0.9000,1,high" {
		t.Errorf("first row = %q, want the highest score", lines[1])
	}
	// A title containing a comma must be quoted, not split into extra columns.
	if lines[2] != `0.1000,0,"low, with comma"` {
		t.Errorf("second row = %q, want the title quoted", lines[2])
	}
}

// fakeDataset installs a two-row labelled dataset in place of the real,
// git-ignored fixture, so calibration is testable anywhere (including CI).
func fakeDataset(t *testing.T) []LabelledStory {
	t.Helper()
	dataset := []LabelledStory{
		{StoryID: 1, Title: "a relevant story", Label: 1},
		{StoryID: 2, Title: "an irrelevant story", Label: 0},
	}
	original := loadDataset
	loadDataset = func(path string) ([]LabelledStory, error) { return dataset, nil }
	t.Cleanup(func() { loadDataset = original })
	return dataset
}

func TestCalibrateFloor(t *testing.T) {
	t.Run("scores every labelled title and writes both outputs", func(t *testing.T) {
		dataset := fakeDataset(t)
		originalScorer := topScoresPerQuery
		defer func() { topScoresPerQuery = originalScorer }()

		var gotTitles []string
		var gotK int
		topScoresPerQuery = func(queries []string, k int) ([][]float64, error) {
			gotTitles, gotK = queries, k
			return [][]float64{{0.8, 0.7}, {0.2, 0.1}}, nil
		}

		var csvOut, reportOut bytes.Buffer
		if err := calibrateFloor(&csvOut, &reportOut); err != nil {
			t.Fatalf("calibrate returned error: %v", err)
		}

		if want := []string{"a relevant story", "an irrelevant story"}; !reflect.DeepEqual(gotTitles, want) {
			t.Errorf("titles embedded = %v, want one query per labelled story %v", gotTitles, want)
		}
		// The pool-cap simulation replays what each story contributes, so
		// calibration must ask for the same k retrieval uses.
		if gotK != rag.RETRIEVAL_TOP_K_PER_STORY {
			t.Errorf("k = %d, want RETRIEVAL_TOP_K_PER_STORY = %d", gotK, rag.RETRIEVAL_TOP_K_PER_STORY)
		}
		// The distribution is built from each story's BEST chunk (element 0),
		// which is what the floor is compared against.
		if !strings.Contains(csvOut.String(), "0.8000,1,a relevant story") {
			t.Errorf("CSV missing the scored row:\n%s", csvOut.String())
		}
		if !strings.Contains(reportOut.String(), "Survival at candidate floors") {
			t.Errorf("report not written to its own stream:\n%s", reportOut.String())
		}
		if !strings.Contains(reportOut.String(), "RECOMMENDATION") {
			t.Errorf("report is missing the verdict section:\n%s", reportOut.String())
		}
		if len(dataset) != 2 {
			t.Fatal("fixture changed unexpectedly")
		}
	})

	t.Run("errors when a title scores against no chunk", func(t *testing.T) {
		fakeDataset(t)
		originalScorer := topScoresPerQuery
		defer func() { topScoresPerQuery = originalScorer }()

		topScoresPerQuery = func(queries []string, k int) ([][]float64, error) {
			return [][]float64{{0.8}, {}}, nil
		}

		if err := calibrateFloor(io.Discard, io.Discard); err == nil {
			t.Error("expected an error when a title has no score at all")
		}
	})

	t.Run("errors when scoring fails", func(t *testing.T) {
		fakeDataset(t)
		originalScorer := topScoresPerQuery
		defer func() { topScoresPerQuery = originalScorer }()

		topScoresPerQuery = func(queries []string, k int) ([][]float64, error) {
			return nil, errors.New("corpus index unavailable")
		}

		if err := calibrateFloor(io.Discard, io.Discard); err == nil {
			t.Error("expected an error when scoring fails")
		}
	})

	t.Run("errors when the scorer returns the wrong count", func(t *testing.T) {
		fakeDataset(t)
		originalScorer := topScoresPerQuery
		defer func() { topScoresPerQuery = originalScorer }()

		topScoresPerQuery = func(queries []string, k int) ([][]float64, error) {
			return [][]float64{{0.5}}, nil // one story scored, two titles sent
		}

		if err := calibrateFloor(io.Discard, io.Discard); err == nil {
			t.Error("expected an error on score/title count mismatch")
		}
	})

	t.Run("errors when the dataset is missing", func(t *testing.T) {
		originalLoad, originalScorer := loadDataset, topScoresPerQuery
		defer func() { loadDataset, topScoresPerQuery = originalLoad, originalScorer }()

		loadDataset = func(path string) ([]LabelledStory, error) {
			return nil, errors.New("no such file")
		}
		topScoresPerQuery = func(queries []string, k int) ([][]float64, error) {
			t.Fatal("scoring should not run without a dataset")
			return nil, nil
		}

		if err := calibrateFloor(io.Discard, io.Discard); err == nil {
			t.Error("expected an error when the dataset is missing")
		}
	})
}

func TestWriteFloorReport(t *testing.T) {
	scored := []scoredTitle{
		{story: LabelledStory{Title: "relevant", Label: 1}, score: 0.8},
		{story: LabelledStory{Title: "irrelevant", Label: 0}, score: 0.2},
	}

	var buf bytes.Buffer
	writeFloorReport(&buf, scored)
	out := buf.String()

	for _, want := range []string{"Best-score distribution by label", "1 (relevant)", "0 (not relevant)", "Survival at candidate floors"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}
