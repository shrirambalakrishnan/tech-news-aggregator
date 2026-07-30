package evalHarness

import (
	"math"
	"testing"

	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
)

const epsilon = 1e-9

func approxEqual(a, b float64) bool { return math.Abs(a-b) < epsilon }

// assertFloat fails the test if got != want within epsilon.
func assertFloat(t *testing.T, name string, got, want float64) {
	t.Helper()
	if !approxEqual(got, want) {
		t.Errorf("%s = %.10f, want %.10f", name, got, want)
	}
}

// TestEvaluateMixedCase is the "show me the math" test: a hand-built dataset
// whose confusion matrix is TP=3, FP=2, FN=1, TN=4, with precision and recall
// worked out by hand.
func TestEvaluateMixedCase(t *testing.T) {
	dataset := []LabelledStory{
		{StoryID: 1, Label: 1, Title: "good1"},   // predicted -> TP
		{StoryID: 2, Label: 1, Title: "good2"},   // predicted -> TP
		{StoryID: 3, Label: 1, Title: "good3"},   // predicted -> TP
		{StoryID: 4, Label: 1, Title: "missed4"}, // NOT predicted -> FN
		{StoryID: 5, Label: 0, Title: "dud5"},    // predicted -> FP
		{StoryID: 6, Label: 0, Title: "dud6"},    // predicted -> FP
		{StoryID: 7, Label: 0, Title: "skip7"},   // NOT predicted -> TN
		{StoryID: 8, Label: 0, Title: "skip8"},   // NOT predicted -> TN
		{StoryID: 9, Label: 0, Title: "skip9"},   // NOT predicted -> TN
		{StoryID: 10, Label: 0, Title: "skip10"}, // NOT predicted -> TN
	}
	predicted := []int{1, 2, 3, 5, 6}

	m := Evaluate(predicted, dataset)

	if m.Confusion != (ConfusionMatrix{TP: 3, FP: 2, TN: 4, FN: 1}) {
		t.Fatalf("confusion = %+v, want {TP:3 FP:2 TN:4 FN:1}", m.Confusion)
	}
	assertFloat(t, "Precision", m.Precision, 3.0/5.0) // 0.6
	assertFloat(t, "Recall", m.Recall, 3.0/4.0)       // 0.75

	// Misclassified lists carry the actual stories, not just counts.
	if len(m.FalsePositives) != 2 || m.FalsePositives[0].StoryID != 5 || m.FalsePositives[1].StoryID != 6 {
		t.Errorf("FalsePositives = %+v, want stories 5 and 6", m.FalsePositives)
	}
	if len(m.FalseNegatives) != 1 || m.FalseNegatives[0].StoryID != 4 {
		t.Errorf("FalseNegatives = %+v, want story 4", m.FalseNegatives)
	}
}

// TestEvaluateEdgeCases covers the corner cases that would divide by zero if
// handled naively: a perfect classifier, predicting nothing, and an empty dataset.
func TestEvaluateEdgeCases(t *testing.T) {
	dataset := []LabelledStory{
		{StoryID: 1, Label: 1, Title: "a"},
		{StoryID: 2, Label: 0, Title: "b"},
		{StoryID: 3, Label: 1, Title: "c"},
		{StoryID: 4, Label: 0, Title: "d"},
	}

	t.Run("perfect classifier", func(t *testing.T) {
		m := Evaluate([]int{1, 3}, dataset) // exactly the positives
		if m.Confusion != (ConfusionMatrix{TP: 2, TN: 2}) {
			t.Fatalf("confusion = %+v, want {TP:2 TN:2}", m.Confusion)
		}
		assertFloat(t, "Precision", m.Precision, 1.0)
		assertFloat(t, "Recall", m.Recall, 1.0)
	})

	t.Run("predicts nothing", func(t *testing.T) {
		m := Evaluate([]int{}, dataset)
		if m.Confusion != (ConfusionMatrix{TN: 2, FN: 2}) {
			t.Fatalf("confusion = %+v, want {TN:2 FN:2}", m.Confusion)
		}
		// TP+FP = 0 and TP+FN > 0 -> precision and recall both 0, no panic/NaN.
		assertFloat(t, "Precision", m.Precision, 0.0)
		assertFloat(t, "Recall", m.Recall, 0.0)
	})

	t.Run("empty dataset", func(t *testing.T) {
		m := Evaluate([]int{}, []LabelledStory{})
		if m.Confusion != (ConfusionMatrix{}) {
			t.Fatalf("confusion = %+v, want all zero", m.Confusion)
		}
		if math.IsNaN(m.Precision) || m.Precision != 0 || math.IsNaN(m.Recall) || m.Recall != 0 {
			t.Errorf("precision/recall = %v/%v, want 0/0 (not NaN)", m.Precision, m.Recall)
		}
	})
}

// TestClassifyInBatches checks the "batch 30" run shape: the dataset is split
// into EVAL_BATCH_SIZE chunks, each classified in one call, and the predicted
// IDs are concatenated. Uses the DI fake so no network call happens.
func TestClassifyInBatches(t *testing.T) {
	dataset := []LabelledStory{
		{StoryID: 1, Title: "a"}, {StoryID: 2, Title: "b"}, {StoryID: 3, Title: "c"},
		{StoryID: 4, Title: "d"}, {StoryID: 5, Title: "e"},
	}

	// Shrink the batch size for the test and restore it afterwards.
	originalBatch := EVAL_BATCH_SIZE
	EVAL_BATCH_SIZE = 2
	defer func() { EVAL_BATCH_SIZE = originalBatch }()

	var batchSizes []int
	classifyTechNewsStory = func(_ hackernews_classifier.Arm, stories []hackernews_classifier.StoryDetail, _ hackernews_classifier.UserProfile) []int {
		batchSizes = append(batchSizes, len(stories))
		// Pretend the classifier flags the first story of every batch.
		return []int{stories[0].Id}
	}
	defer func() { classifyTechNewsStory = hackernews_classifier.ClassifyTechNewsStory }()

	predicted := classifyInBatches(hackernews_classifier.ArmGeneric, dataset, hackernews_classifier.UserProfile{})

	// 5 stories at batch size 2 -> chunks of 2, 2, 1.
	wantSizes := []int{2, 2, 1}
	if len(batchSizes) != len(wantSizes) {
		t.Fatalf("made %d calls, want %d (sizes %v)", len(batchSizes), len(wantSizes), batchSizes)
	}
	for i, want := range wantSizes {
		if batchSizes[i] != want {
			t.Errorf("batch %d size = %d, want %d", i, batchSizes[i], want)
		}
	}

	// First story of each chunk: IDs 1, 3, 5.
	want := []int{1, 3, 5}
	if len(predicted) != len(want) {
		t.Fatalf("predicted = %v, want %v", predicted, want)
	}
	for i := range want {
		if predicted[i] != want[i] {
			t.Errorf("predicted[%d] = %d, want %d", i, predicted[i], want[i])
		}
	}
}
