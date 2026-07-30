package evalHarness

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
	"github.com/shrirambalakrishnan/tech-news/profile"
)

// errArmNotImplemented backs the arm 2 stub. Its own variable so tests can
// assert on it without matching the message text.
var errArmNotImplemented = fmt.Errorf("arm 2 (RAG): corpus chunks unavailable - not implemented")

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

// RunEval is the `go run . eval <arm>` entrypoint: load the labelled data, run
// the classifier over it in batches exactly as production would under the given
// arm, score the predictions against the human labels, and print the report to
// the console. It runs exactly one arm.
func RunEval(arm hackernews_classifier.Arm) {
	dataset, err := loadLabelledData(LABELLED_DATA_FILE)
	if err != nil {
		log.Fatal("eval: failed to load labelled data: ", err)
	}
	log.Printf("eval: loaded %d labelled stories from %s", len(dataset), LABELLED_DATA_FILE)

	// Build the profile the arm requires. Unlike the old fail-soft path, a
	// missing artifact under arm 1 is fatal - the eval must not silently score
	// arm 0 (see issue #11 acceptance criteria).
	userProfile, err := buildProfileForArm(arm)
	if err != nil {
		log.Fatal("eval: ", err)
	}

	predictedIDs := classifyInBatches(arm, dataset, userProfile)

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

// buildProfileForArm initializes the UserProfile the chosen arm requires,
// erroring (rather than failing soft) when the arm's data is missing so the eval
// exits non-zero instead of silently scoring a different arm. Mirrors main's
// buildProfileForArm.
//
//   - ArmGeneric   -> empty profile; the generic flow needs no user data.
//   - ArmInterests -> profile from the distilled JSON; errors if it's absent.
//   - ArmRAG       -> stubbed this iteration; always errors.
func buildProfileForArm(arm hackernews_classifier.Arm) (hackernews_classifier.UserProfile, error) {
	switch arm {
	case hackernews_classifier.ArmGeneric:
		return hackernews_classifier.UserProfile{}, nil
	case hackernews_classifier.ArmInterests:
		userContext, err := loadUserContext(profile.USER_CONTEXT_FILE)
		if err != nil {
			return hackernews_classifier.UserProfile{}, fmt.Errorf("arm 1 (interests): distilled user profile unavailable at %s (run `go run . prebuild`): %w", profile.USER_CONTEXT_FILE, err)
		}
		return hackernews_classifier.UserProfile{
			Summary:   userContext.Summary,
			Interests: userContext.Interests,
		}, nil
	case hackernews_classifier.ArmRAG:
		return hackernews_classifier.UserProfile{}, errArmNotImplemented
	default:
		return hackernews_classifier.UserProfile{}, fmt.Errorf("unknown arm: %d", arm)
	}
}

// classifyInBatches walks the dataset in EVAL_BATCH_SIZE chunks, classifies each
// chunk in one Claude call under the given arm, and concatenates the
// predicted-relevant IDs. This is the "batch 30" run shape: each call is the
// same size production sends.
func classifyInBatches(arm hackernews_classifier.Arm, dataset []LabelledStory, userProfile hackernews_classifier.UserProfile) []int {
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

		ids := classifyTechNewsStory(arm, stories, userProfile)
		predicted = append(predicted, ids...)

		log.Printf("eval: classified batch %d-%d (%d stories) -> %d flagged relevant",
			start, end-1, len(batch), len(ids))
	}

	return predicted
}
