package hackernews_classifier

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"

	"github.com/shrirambalakrishnan/tech-news/claudeapi"
)

// Arm selects, explicitly, which classification flow runs. It replaces the old
// implicit selection (which branched on whether a profile was present on disk).
// The caller decides the arm; nothing on disk overrides it. See issue #11.
type Arm int

const (
	// ArmGeneric classifies against the hardcoded static "technical computer
	// science" ruleset with no user profile. Production default.
	ArmGeneric Arm = 0
	// ArmInterests classifies against the user's distilled interest profile
	// (summary + interests) injected into the prompt.
	ArmInterests Arm = 1
	// ArmRAG classifies against raw corpus excerpts retrieved for the current
	// batch of stories (embeddings over profile/corpus). The caller fills
	// UserProfile.RetrievedExcerpts before classifying.
	ArmRAG Arm = 2
	// ArmRAGPerStory classifies against the SAME kind of corpus excerpts as
	// ArmRAG, retrieved differently: one query per story, pooled, instead of one
	// blended query for the whole batch. It differs from ArmRAG in exactly one
	// variable - how excerpts are selected - so a delta in the eval is
	// attributable to retrieval and not to a changed prompt. The prompt is
	// therefore deliberately shared with ArmRAG rather than written afresh.
	ArmRAGPerStory Arm = 3
)

type StoryDetail struct {
	Id    int
	Title string
}

// UserProfile is the classifier's input contract for the user's interests. It
// carries only the signal fields the classifier reasons over - never the
// provenance metadata that lives on profile.UserContext. Add new signal fields
// here (e.g. Languages, Topics) without churning any call site's signature; just
// extend IsEmpty and the mapping in the caller.
type UserProfile struct {
	Summary   string
	Interests []string
	// RetrievedExcerpts carries the retrieval arms' context: the corpus chunks
	// the caller retrieved for THIS batch of stories - not the corpus, and not a
	// cached copy of the index. Every element is inlined verbatim into the
	// system prompt, so its length is the per-call token cost; that is why the
	// caller retrieves a handful rather than loading profile/corpus_index.json
	// (sending the whole corpus every 4h is the cost Approach 3 exists to
	// avoid). Unlike Summary/Interests, which are built once per run, this is
	// per classify call - the retrieval query is the batch's own titles.
	// Only ArmRAG and ArmRAGPerStory read it, and they read it identically: the
	// arm, not this field's emptiness, selects the prompt, so each arm builds
	// from exactly one context source and the arms stay comparable in the eval.
	// The two differ only in how the caller filled this slice - ArmRAG from one
	// blended query, ArmRAGPerStory from per-story queries pooled together.
	RetrievedExcerpts []string
}

// IsEmpty reports whether the profile carries no usable signal. Since flow
// selection is now explicit (via Arm), callers use this as a defensive check -
// e.g. to reject an ArmInterests run whose loaded profile turned out empty.
// Centralized so adding signal fields updates the check in one place.
func (p UserProfile) IsEmpty() bool {
	return p.Summary == "" && len(p.Interests) == 0 && len(p.RetrievedExcerpts) == 0
}

var constructPromptSystemAttribute = ConstructPromptSystemAttribute
var constructPromptMessageAttribute = ConstructPromptMessageAttribute
var claudeMessageApiCall = claudeapi.ClaudeMessageApiCall

// ConstructPromptSystemAttribute builds the classifier system prompt for the
// chosen arm: ArmGeneric returns the static "technical computer science"
// ruleset, ArmInterests classifies against the user's distilled interest
// profile, and ArmRAG/ArmRAGPerStory classify against corpus excerpts retrieved
// for the current batch. The arm drives the flow explicitly - neither the
// profile's emptiness nor the presence of corpus chunks selects it - so each
// prompt is built from exactly one context source and the arms stay comparable.
//
// ArmRAG and ArmRAGPerStory share a branch on purpose. They differ only in how
// the excerpts were selected, which happens upstream in armcontext; giving arm 3
// its own prompt would make any eval delta unattributable between "per-story
// retrieval helped" and "we happened to write a better prompt".
func ConstructPromptSystemAttribute(arm Arm, profile UserProfile) string {
	switch arm {
	case ArmInterests:
		return interestsClassificationPrompt(profile)
	case ArmRAG, ArmRAGPerStory:
		return ragClassificationPrompt(profile.RetrievedExcerpts)
	default:
		return staticClassificationPrompt()
	}
}

// interestsClassificationPrompt builds the ArmInterests system prompt: the
// distilled summary + interests injected as the reader's profile.
func interestsClassificationPrompt(profile UserProfile) string {
	interests := strings.Join(profile.Interests, ", ")

	return `You are a Hacker News story classifier that selects stories matching a specific reader's technical interests.

Reader profile:
` + profile.Summary + `

The reader is interested in topics such as: ` + interests + `

Classify a story as relevant if its title indicates it covers one of these interests or a closely related technical topic. Generalize sensibly from the profile - a listed interest implies adjacent subtopics within the same domain - but do NOT include stories that merely sit in the broad software industry without matching the reader's specific interests.

Do NOT classify as relevant:
- Tech industry news, business, fundraising, or hiring
- Tech policy, regulation, or privacy law
- Science that is not computer science (physics, biology, space)
- General-interest or cultural stories, even if tech-adjacent
- Topics outside the reader's interests above

Return ONLY a JSON array of story IDs that are relevant.
No explanation, no markdown fences, no wrapping.
Example response: [123, 456, 789]
`
}

// ragClassificationPrompt builds the ArmRAG system prompt: instead of a
// distilled profile it presents raw corpus excerpts retrieved for this batch
// and asks Claude to infer the reader's interests from them. The negative
// rules and the output contract deliberately mirror the interests prompt so the
// eval arms differ only in their context source.
func ragClassificationPrompt(excerpts []string) string {
	rendered := ""
	for i, excerpt := range excerpts {
		rendered += "--- Excerpt " + strconv.Itoa(i+1) + " ---\n" + excerpt + "\n\n"
	}

	return `You are a Hacker News story classifier that selects stories matching a specific reader's technical interests.

The excerpts below are taken from documents the reader collected: repository READMEs, blog posts, and white papers they are interested in. Treat them as evidence of the reader's technical interests.

` + rendered + `Classify a story as relevant if its title indicates it covers a topic these excerpts show the reader cares about, or a closely related technical topic. Generalize sensibly - an excerpt implies interest in adjacent subtopics within the same domain - but do NOT include stories that merely sit in the broad software industry without matching these interests.

Do NOT classify as relevant:
- Tech industry news, business, fundraising, or hiring
- Tech policy, regulation, or privacy law
- Science that is not computer science (physics, biology, space)
- General-interest or cultural stories, even if tech-adjacent
- Topics outside the interests evidenced above

Return ONLY a JSON array of story IDs that are relevant.
No explanation, no markdown fences, no wrapping.
Example response: [123, 456, 789]
`
}

func staticClassificationPrompt() string {
	return `You are a Hacker News story classifier.

	Classify stories as "technical computer science" if they are about:
	- Programming languages, compilers, interpreters
	- Systems programming, operating systems, databases
	- Networking, distributed systems, infrastructure
	- Algorithms, data structures, computer science theory
	- Developer tools, CLI tools, libraries, frameworks
	- Security, cryptography, reverse engineering
	- Hardware hacking, embedded systems, CPUs

	Do NOT classify as technical:
	- Tech industry news, business, fundraising
	- Tech policy, regulation, privacy law
	- Science that is not computer science (physics, biology, space)
	- Design, typography, UX (unless about the underlying tech)
	- Career advice, hiring, workplace culture
	- Historical or cultural stories even if tech-adjacent

	Return ONLY a JSON array of story IDs that are technical.
	No explanation, no markdown fences, no wrapping.
	Example response: [123, 456, 789]
	`
}

func ConstructPromptMessageAttribute(stories []StoryDetail) string {
	prompt := ""

	for i := 0; i < len(stories); i++ {
		prompt += "Story Id: " + strconv.Itoa(stories[i].Id) + ", Story Title: " + stories[i].Title + "\n"
	}

	return prompt
}

func ClassifyTechNewsStory(arm Arm, stories []StoryDetail, profile UserProfile) []int {
	var classificationPrompt claudeapi.PromptInput
	classificationPrompt.System = constructPromptSystemAttribute(arm, profile)
	classificationPrompt.Message = constructPromptMessageAttribute(stories)

	var response claudeapi.Response

	err := claudeMessageApiCall(classificationPrompt, &response)
	if err != nil {
		log.Println("Error calling Claude API:", err)
		return []int{}
	}

	log.Println("Classification Response:", response)

	if len(response.Content) == 0 {
		log.Println("Empty content in classification response")
		return []int{}
	}

	techNewsStoryIdsStr := response.Content[0].Text
	log.Println("techNewsStoryIdsStr = ", techNewsStoryIdsStr)

	techNewsStoryIds := []int{}
	err = json.Unmarshal([]byte(extractJSONArray(techNewsStoryIdsStr)), &techNewsStoryIds)
	if err != nil {
		log.Println("Error unmarshalling tech news story IDs:", err)
		return []int{}
	}

	log.Println("techNewsStoryIds = ", techNewsStoryIds)
	return techNewsStoryIds
}

// extractJSONArray pulls the JSON array out of a model response, tolerating the
// wrappers every prompt here asks Claude NOT to emit: ```json fences, a "Here
// are the IDs:" preamble, trailing commentary.
//
// The instruction alone is not enough. Arm 2 in particular inlines corpus
// excerpts - READMEs and papers full of fenced code - and the model mirrors that
// formatting back, so it wraps the array in a fence and the strict Unmarshal
// fails. A parse failure is *silent* here (the caller logs and returns no IDs),
// which in the eval reads as recall 0 rather than as an error - so tolerating
// the wrapper is worth more than being strict about it.
//
// Slicing first '[' to last ']' rather than stripping fences specifically keeps
// this indifferent to which wrapper the model picked. The IDs are a flat array,
// so there is no nesting to confuse the bounds. Returns "" when there is no
// array at all, which fails the Unmarshal exactly as before.
func extractJSONArray(text string) string {
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start < 0 || end < start {
		return ""
	}
	return text[start : end+1]
}
