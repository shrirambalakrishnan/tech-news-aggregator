package hackernews_classifier

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"

	"github.com/shrirambalakrishnan/tech-news/claudeapi"
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
	// CorpusChunks carries Approach 3 (RAG) context: corpus excerpts retrieved
	// for the current batch of stories. When present it takes precedence over
	// Summary/Interests in prompt construction, so each eval arm stays pure —
	// a prompt is built from exactly one context source.
	CorpusChunks []string
}

// IsEmpty reports whether the profile carries no usable signal, in which case
// callers fall back to the static classification rules. Centralized so adding
// fields updates the fail-soft check in one place.
func (p UserProfile) IsEmpty() bool {
	return p.Summary == "" && len(p.Interests) == 0 && len(p.CorpusChunks) == 0
}

var constructPromptSystemAttribute = ConstructPromptSystemAttribute
var constructPromptMessageAttribute = ConstructPromptMessageAttribute
var claudeMessageApiCall = claudeapi.ClaudeMessageApiCall

// ConstructPromptSystemAttribute builds the classifier system prompt, choosing
// the richest context source available: retrieved corpus chunks (Approach 3 /
// RAG) over the distilled profile (Approach 2) over the static ruleset
// (Approach 1). Each prompt uses exactly one source so the approaches stay
// comparable in the eval.
func ConstructPromptSystemAttribute(profile UserProfile) string {
	if len(profile.CorpusChunks) > 0 {
		return ragClassificationPrompt(profile.CorpusChunks)
	}
	if profile.IsEmpty() {
		return staticClassificationPrompt()
	}

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

// ragClassificationPrompt builds the Approach 3 system prompt: instead of a
// distilled profile it presents raw corpus excerpts retrieved for this batch
// and asks Claude to infer the reader's interests from them. The negative
// rules and the output contract deliberately mirror the profile prompt so the
// eval arms differ only in their context source.
func ragClassificationPrompt(chunks []string) string {
	excerpts := ""
	for i, chunk := range chunks {
		excerpts += "--- Excerpt " + strconv.Itoa(i+1) + " ---\n" + chunk + "\n\n"
	}

	return `You are a Hacker News story classifier that selects stories matching a specific reader's technical interests.

The excerpts below are taken from documents the reader collected: repository READMEs, blog posts, and white papers they are interested in. Treat them as evidence of the reader's technical interests.

` + excerpts + `Classify a story as relevant if its title indicates it covers a topic these excerpts show the reader cares about, or a closely related technical topic. Generalize sensibly - an excerpt implies interest in adjacent subtopics within the same domain - but do NOT include stories that merely sit in the broad software industry without matching these interests.

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

func ClassifyTechNewsStory(stories []StoryDetail, profile UserProfile) []int {
	var classificationPrompt claudeapi.PromptInput
	classificationPrompt.System = constructPromptSystemAttribute(profile)
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
	err = json.Unmarshal([]byte(techNewsStoryIdsStr), &techNewsStoryIds)
	if err != nil {
		log.Println("Error unmarshalling tech news story IDs:", err)
		return []int{}
	}

	log.Println("techNewsStoryIds = ", techNewsStoryIds)
	return techNewsStoryIds
}
