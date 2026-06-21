package evalHarness

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
	"github.com/shrirambalakrishnan/tech-news/profile"
)

// LABELLED_DATA_FILE is the hand-labelled dataset (git-ignored). Path is
// relative to the repo root, where `go run . eval` executes.
const LABELLED_DATA_FILE = "evalHarness/hn-responses-labelled.json"

// EVAL_BATCH_SIZE is how many stories we send to Claude per classify call. We
// match production's per-call size (~30, one front page) so the eval measures
// the same prompt shape we actually ship, and so the returned JSON ID array
// stays well under claudeapi's MaxTokens cap. See CLAUDE.md -> Eval harness.
// It's a mutable package var so tests can shrink it.
var EVAL_BATCH_SIZE = 30

// DI seams (same function-variable convention as the rest of the repo): tests
// swap these for fakes so the runner never touches the network or the disk.
var classifyTechNewsStory = hackernews_classifier.ClassifyTechNewsStory
var loadUserContext = profile.LoadUserContext

// RunEval is the `go run . eval` entrypoint: load the labelled data, run the
// classifier over it in batches exactly as production would, score the
// predictions against the human labels, and print the report to the console.
func RunEval() {
	dataset, err := loadLabelledData(LABELLED_DATA_FILE)
	if err != nil {
		log.Fatal("eval: failed to load labelled data: ", err)
	}
	log.Printf("eval: loaded %d labelled stories from %s", len(dataset), LABELLED_DATA_FILE)

	// Mirror the production classify path: load the prebuilt interest profile and
	// fail soft to the static rules if it's missing (prebuild may not have run).
	userProfile := loadProfileOrEmpty()

	predictedIDs := classifyInBatches(dataset, userProfile)

	metrics := Evaluate(predictedIDs, dataset)

	// Console-log the full report (every metric + the FP/FN title lists).
	fmt.Print(metrics.Report())
}

// loadLabelledData reads and parses the JSON array of labelled stories.
func loadLabelledData(path string) ([]LabelledStory, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var dataset []LabelledStory
	if err := json.Unmarshal(raw, &dataset); err != nil {
		return nil, err
	}
	return dataset, nil
}

// loadProfileOrEmpty returns the user's interest profile, or an empty profile
// (which makes the classifier fall back to the static rules) when the prebuilt
// artifact is missing. Same fail-soft behaviour as FilterHackerNewsStoriesByTitle.
func loadProfileOrEmpty() hackernews_classifier.UserProfile {
	userContext, err := loadUserContext(profile.USER_CONTEXT_FILE)
	if err != nil {
		log.Println("eval: user context unavailable, using static classification rules:", err)
		return hackernews_classifier.UserProfile{}
	}
	return hackernews_classifier.UserProfile{
		Summary:   userContext.Summary,
		Interests: userContext.Interests,
	}
}

// classifyInBatches walks the dataset in EVAL_BATCH_SIZE chunks, classifies each
// chunk in one Claude call, and concatenates the predicted-relevant IDs. This is
// the "batch 30" run shape: each call is the same size production sends.
func classifyInBatches(dataset []LabelledStory, userProfile hackernews_classifier.UserProfile) []int {
	var predicted []int

	for start := 0; start < len(dataset); start += EVAL_BATCH_SIZE {
		end := start + EVAL_BATCH_SIZE
		if end > len(dataset) {
			end = len(dataset)
		}
		batch := dataset[start:end]

		// Map the labelled rows to the classifier's input contract (id + title only).
		stories := make([]hackernews_classifier.StoryDetail, 0, len(batch))
		for _, s := range batch {
			stories = append(stories, hackernews_classifier.StoryDetail{Id: s.StoryID, Title: s.Title})
		}

		ids := classifyTechNewsStory(stories, userProfile)
		predicted = append(predicted, ids...)

		log.Printf("eval: classified batch %d-%d (%d stories) -> %d flagged relevant",
			start, end-1, len(batch), len(ids))
	}

	return predicted
}
