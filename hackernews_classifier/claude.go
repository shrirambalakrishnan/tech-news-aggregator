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
	// ArmRAG is the future RAG/embedding flow. Stubbed this iteration; the
	// profile-init step errors before the classifier is ever reached.
	ArmRAG Arm = 2
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

// IsEmpty reports whether the profile carries no usable signal. Since flow
// selection is now explicit (via Arm), callers use this as a defensive check -
// e.g. to reject an ArmInterests run whose loaded profile turned out empty.
// Centralized so adding signal fields updates the check in one place.
func (p UserProfile) IsEmpty() bool {
	return p.Summary == "" && len(p.Interests) == 0
}

var constructPromptSystemAttribute = ConstructPromptSystemAttribute
var constructPromptMessageAttribute = ConstructPromptMessageAttribute
var claudeMessageApiCall = claudeapi.ClaudeMessageApiCall

// ConstructPromptSystemAttribute builds the classifier system prompt for the
// chosen arm. ArmGeneric returns the static "technical computer science"
// ruleset; ArmInterests classifies against the user's actual interests. The arm
// drives the flow explicitly - the profile's emptiness no longer selects it.
func ConstructPromptSystemAttribute(arm Arm, profile UserProfile) string {
	if arm == ArmGeneric {
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
	err = json.Unmarshal([]byte(techNewsStoryIdsStr), &techNewsStoryIds)
	if err != nil {
		log.Println("Error unmarshalling tech news story IDs:", err)
		return []int{}
	}

	log.Println("techNewsStoryIds = ", techNewsStoryIds)
	return techNewsStoryIds
}
