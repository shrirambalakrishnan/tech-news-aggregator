package main

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
)

const (
	ANTHROPIC_API_HOST         = "https://api.anthropic.com/"
	ANTHROPIC_API_VERSION      = "v1"
	ANTHROPIC_MESSAGE_ENDPOINT = "messages"
	ANTHROPIC_MESSAGE_URL      = ANTHROPIC_API_HOST + ANTHROPIC_API_VERSION + "/" + ANTHROPIC_MESSAGE_ENDPOINT

	ANATHROPIC_MODEL_NAME = "claude-haiku-4-5-20251001"
)

type StoryDetail struct {
	Id    int
	Title string
}

var constructPromptSystemAttribute = ConstructPromptSystemAttribute
var constructPromptMessageAttribute = ConstructPromptMessageAttribute
var claudeMessageApiCall = ClaudeMessageApiCall

func ConstructPromptSystemAttribute() string {
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

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type ClassificationRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system"`
	Messages  []Message `json:"messages"`
}

type ResponseContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ClassificationResponse struct {
	Id        string            `json:"id"`
	Type      string            `json:"type"`
	Role      string            `json:"role"`
	Content   []ResponseContent `json:"content"`
	Model     string            `json:"model"`
	CreatedAt int64             `json:"created_at"`
}

type PromptInput struct {
	System  string
	Message string
}

func ClassifyTechNewsStory(stories []StoryDetail) []int {
	var classificationPrompt PromptInput
	classificationPrompt.System = constructPromptSystemAttribute()
	classificationPrompt.Message = constructPromptMessageAttribute(stories)

	var classificationResponse ClassificationResponse

	err := claudeMessageApiCall(classificationPrompt, &classificationResponse)
	if err != nil {
		log.Println("Error calling Claude API:", err)
		return []int{}
	}

	log.Println("Classification Response:", classificationResponse)

	if len(classificationResponse.Content) == 0 {
		log.Println("Empty content in classification response")
		return []int{}
	}

	techNewsStoryIdsStr := classificationResponse.Content[0].Text
	log.Println("techNewsStoryIdsStr = ", techNewsStoryIdsStr)

	techNewsStoryIds := []int{}
	// convert techNewsStoryIdsStr (which is a string representation of an array) to an array of ints
	err = json.Unmarshal([]byte(techNewsStoryIdsStr), &techNewsStoryIds)
	if err != nil {
		log.Println("Error unmarshalling tech news story IDs:", err)
		return []int{}
	}

	log.Println("techNewsStoryIds = ", techNewsStoryIds)
	return techNewsStoryIds

}

func ClaudeMessageApiCall(prompt PromptInput, classificationResponse *ClassificationResponse) error {

	message := Message{
		Role:    "user",
		Content: prompt.Message,
	}
	reqBody := ClassificationRequest{
		Model:     ANATHROPIC_MODEL_NAME,
		MaxTokens: 1024,
		System:    prompt.System,
		Messages:  []Message{message},
	}
	log.Println("reqBody = ", reqBody)

	customHeaders := map[string]string{
		"Content-Type":      "application/json",
		"X-Api-Key":         os.Getenv("ANTHROPIC_API_KEY"),
		"anthropic-version": "2023-06-01",
	}

	postjsonErr := PostJSON(ANTHROPIC_MESSAGE_URL, customHeaders, reqBody, classificationResponse)

	if postjsonErr != nil {
		panic(postjsonErr)
	}

	return nil
}
