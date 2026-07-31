// Package armcontext builds the classifier context each experiment arm needs.
//
// It exists so the scheduled run (main) and the offline eval (evalHarness) build
// arm context from ONE implementation instead of two hand-synced copies. The
// eval's numbers are only transferable if it feeds the classifier exactly what
// production feeds it - the same profile mapping and, for arm 2, the same
// retrieval query - and that invariant belongs to the compiler, not to a comment
// in each package.
//
// Dependency direction is unchanged: armcontext -> {hackernews_classifier,
// profile, rag}, with main and evalHarness on top. Nothing lower imports it.
package armcontext

import (
	"fmt"
	"strings"

	"github.com/shrirambalakrishnan/tech-news/hackernews_classifier"
	"github.com/shrirambalakrishnan/tech-news/profile"
	"github.com/shrirambalakrishnan/tech-news/rag"
)

// DI seams (the repo-wide function-variable convention): tests swap these for
// fakes so building a profile never touches the disk or the network.
var loadUserContext = profile.LoadUserContext
var loadRagContext = rag.RetrieveContext

// BuildProfile returns the UserProfile the chosen arm classifies against, built
// for THIS batch of stories.
//
//   - ArmGeneric   -> empty profile; the static ruleset needs no user data.
//   - ArmInterests -> summary + interests from the distilled JSON.
//   - ArmRAG       -> the top-k corpus excerpts retrieved for these stories.
//
// The stories are the arm 2 retrieval query, which is why they are a parameter
// rather than something the caller splices in afterwards: every arm's context is
// built here, in one switch, so there is exactly one place that answers "what
// does this arm classify against?".
//
// Every failure is an error, never a degraded profile: an arm asked for must run
// as that arm or not at all. Silently falling back (arm 2 -> arm 1 -> arm 0) is
// what issue #11 exists to prevent - it publishes numbers labelled "RAG" that
// were really the static ruleset.
func BuildProfile(arm hackernews_classifier.Arm, stories []hackernews_classifier.StoryDetail) (hackernews_classifier.UserProfile, error) {
	switch arm {
	case hackernews_classifier.ArmGeneric:
		return hackernews_classifier.UserProfile{}, nil

	case hackernews_classifier.ArmInterests:
		userContext, err := loadUserContext(profile.USER_CONTEXT_FILE)
		if err != nil {
			return hackernews_classifier.UserProfile{}, fmt.Errorf("arm 1 (interests): distilled user profile unavailable at %s (run `go run . prebuild`): %w", profile.USER_CONTEXT_FILE, err)
		}
		// Provenance metadata on UserContext is deliberately dropped: the
		// classifier's contract carries signal fields only.
		return hackernews_classifier.UserProfile{
			Summary:   userContext.Summary,
			Interests: userContext.Interests,
		}, nil

	case hackernews_classifier.ArmRAG:
		// Only the RETRIEVAL_TOP_K most similar chunks - they get inlined
		// verbatim into the prompt, so this is a handful of excerpts and never
		// the whole index (re-sending the corpus every 4h is the cost Approach 3
		// exists to avoid).
		excerpts, err := loadRagContext(retrievalQuery(stories), rag.RETRIEVAL_TOP_K)
		if err != nil {
			return hackernews_classifier.UserProfile{}, fmt.Errorf("arm 2 (RAG): corpus retrieval failed (run `go run . embed`): %w", err)
		}
		return hackernews_classifier.UserProfile{RetrievedExcerpts: excerpts}, nil

	default:
		return hackernews_classifier.UserProfile{}, fmt.Errorf("unknown arm: %d", arm)
	}
}

// retrievalQuery is arm 2's retrieval key: the batch's story titles, newline
// joined. One query per batch rather than one per title - 30 paced Voyage calls
// would be far slower and the batch prompt pools the excerpts anyway.
//
// Using the titles as the query is the known confirmation-bias trade-off (see
// CLAUDE.md, the retrieval-key problem): it biases retrieval toward context that
// confirms the batch, so the eval watches FP/precision, not just recall.
func retrievalQuery(stories []hackernews_classifier.StoryDetail) string {
	titles := make([]string, 0, len(stories))
	for _, story := range stories {
		titles = append(titles, story.Title)
	}
	return strings.Join(titles, "\n")
}
