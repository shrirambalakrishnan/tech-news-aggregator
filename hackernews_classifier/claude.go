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
}

// IsEmpty reports whether the profile carries no usable signal, in which case
// callers fall back to the static classification rules. Centralized so adding
// fields updates the fail-soft check in one place.
func (p UserProfile) IsEmpty() bool {
	return p.Summary == "" && len(p.Interests) == 0
}

var constructPromptSystemAttribute = ConstructPromptSystemAttribute
var constructPromptMessageAttribute = ConstructPromptMessageAttribute
var claudeMessageApiCall = claudeapi.ClaudeMessageApiCall

// ConstructPromptSystemAttribute builds the classifier system prompt. When the
// profile carries no signal (prebuild hasn't run, or the artifact is missing) it
// falls back to the static "technical computer science" ruleset. Otherwise it
// classifies stories against the user's actual interests.
func ConstructPromptSystemAttribute(profile UserProfile) string {
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
