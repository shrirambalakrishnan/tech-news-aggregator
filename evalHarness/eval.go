package evalHarness

import (
	"fmt"
	"strings"
)

// LabelledStory is one row of the hand-labelled dataset
// (evalHarness/hn-responses-labelled.json). `Label` is the ground truth a human
// assigned to each story while building the dataset:
//
//	1 = relevant  ("a story worth reading" — a positive)
//	0 = not relevant                       (a negative)
//
// The eval compares the classifier's predictions against this human label to
// see how good the classifier is.
type LabelledStory struct {
	ObjectID string `json:"objectID"`
	StoryID  int    `json:"story_id"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Label    int    `json:"label"`
}

// ConfusionMatrix is the 2x2 table that everything is built from. "Positive" =
// the classifier said "relevant"; "True/False" = whether it was right. Read each
// name as <was-the-prediction-right><what-it-predicted>:
//
//	                    | actually relevant (label 1) | actually not (label 0)
//	------------------- | --------------------------- | ----------------------
//	predicted relevant  |        TP (hit)             |     FP (false alarm)
//	predicted not       |        FN (miss)            |     TN (correct reject)
//
// These four cells carry all the counts: total = TP+FP+TN+FN, and the number of
// truly-relevant stories = TP+FN.
type ConfusionMatrix struct {
	TP int // True  Positive: predicted relevant AND it was   -> a correct catch
	FP int // False Positive: predicted relevant BUT it wasn't -> a false alarm (you read a dud)
	TN int // True  Negative: predicted not      AND it wasn't -> a correct skip
	FN int // False Negative: predicted not      BUT it was    -> a miss (you skipped a good one)
}

// Metrics is the bare-minimum eval report: the confusion matrix, the two rates
// that actually tell you if an imbalanced classifier works (precision & recall),
// and the actual stories it got wrong.
type Metrics struct {
	Confusion ConfusionMatrix

	// Precision = TP/(TP+FP): of the stories we FLAGGED, how many were actually
	// good. Measures false alarms — how much you can trust a "relevant" verdict.
	Precision float64

	// Recall = TP/(TP+FN): of the stories that WERE good, how many we caught.
	// Measures misses — independent of precision (a different failure mode).
	Recall float64

	// The actual mistakes, not just counts — the most useful output for tuning
	// the classifier (the roadmap's whole point).
	FalsePositives []LabelledStory // flagged as relevant but labelled 0 -> rules too eager
	FalseNegatives []LabelledStory // labelled relevant but we skipped    -> blind spots
}

// Evaluate is the heart of the eval and is intentionally PURE: it takes the
// classifier's predicted-relevant IDs plus the labelled dataset and returns the
// metrics. No network, no I/O -> trivially unit-testable with hand-built numbers
// (see eval_test.go).
//
// A story counts as "predicted positive" if and only if its StoryID appears in
// predictedPositiveIDs; every other story in the dataset is "predicted negative".
func Evaluate(predictedPositiveIDs []int, dataset []LabelledStory) Metrics {
	// Put the predicted IDs in a set for O(1) lookup while we walk the dataset.
	predicted := make(map[int]bool, len(predictedPositiveIDs))
	for _, id := range predictedPositiveIDs {
		predicted[id] = true
	}

	var m Metrics
	for _, s := range dataset {
		predictedPositive := predicted[s.StoryID]
		actualPositive := s.Label == 1

		switch {
		case predictedPositive && actualPositive:
			m.Confusion.TP++ // we flagged it and it was good
		case predictedPositive && !actualPositive:
			m.Confusion.FP++ // we flagged it but it was a dud
			m.FalsePositives = append(m.FalsePositives, s)
		case !predictedPositive && !actualPositive:
			m.Confusion.TN++ // we skipped it and it was right to skip
		default: // !predictedPositive && actualPositive
			m.Confusion.FN++ // we skipped it but it was actually good
			m.FalseNegatives = append(m.FalseNegatives, s)
		}
	}

	tp := float64(m.Confusion.TP)
	fp := float64(m.Confusion.FP)
	fn := float64(m.Confusion.FN)
	// Guard the denominators: if the classifier flagged nothing, TP+FP=0 and
	// precision is undefined — report 0 instead of NaN (sklearn's convention).
	if tp+fp > 0 {
		m.Precision = tp / (tp + fp)
	}
	if tp+fn > 0 {
		m.Recall = tp / (tp + fn)
	}

	return m
}

// Report renders the metrics as a human-readable block for the console. Every
// line is labelled so you can read it straight off the terminal.
func (m Metrics) Report() string {
	var b strings.Builder

	fmt.Fprintln(&b, "================ EVAL REPORT ================")

	fmt.Fprintln(&b, "\n-- Confusion matrix --")
	fmt.Fprintf(&b, "  TP (hit, flagged & relevant)        = %d\n", m.Confusion.TP)
	fmt.Fprintf(&b, "  FP (false alarm, flagged but dud)   = %d\n", m.Confusion.FP)
	fmt.Fprintf(&b, "  TN (correct skip)                   = %d\n", m.Confusion.TN)
	fmt.Fprintf(&b, "  FN (miss, skipped but relevant)     = %d\n", m.Confusion.FN)

	fmt.Fprintln(&b, "\n-- Metrics --")
	fmt.Fprintf(&b, "  Precision (of flagged, %% good)      = %.4f\n", m.Precision)
	fmt.Fprintf(&b, "  Recall    (of good, %% caught)       = %.4f\n", m.Recall)

	fmt.Fprintf(&b, "\n-- False positives (flagged but labelled NOT relevant): %d --\n", len(m.FalsePositives))
	for _, s := range m.FalsePositives {
		fmt.Fprintf(&b, "  [%d] %s\n", s.StoryID, s.Title)
	}

	fmt.Fprintf(&b, "\n-- False negatives (relevant but we MISSED): %d --\n", len(m.FalseNegatives))
	for _, s := range m.FalseNegatives {
		fmt.Fprintf(&b, "  [%d] %s\n", s.StoryID, s.Title)
	}

	fmt.Fprintln(&b, "\n=============================================")
	return b.String()
}
