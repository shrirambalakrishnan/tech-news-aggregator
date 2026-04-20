package main

import "testing"

func TestConstructPromptSystemAttribute(t *testing.T) {

	t.Run("test the static System Promt", func(t *testing.T) {

		expectedResponse := `You are a Hacker News story classifier.

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

		systemPromptResponse := ConstructPromptSystemAttribute()

		if systemPromptResponse != expectedResponse {
			t.Fatalf("expected system prompt to be %s, got %s", expectedResponse, systemPromptResponse)
		}
	})

}

func TestConstructPromptMessageAttribute(t *testing.T) {

	t.Run("test response has correct format of prompt", func(t *testing.T) {

		storiesParam := []StoryDetail{
			{Id: 1, Title: "Story1"},
			{Id: 2, Title: "Story2"},
			{Id: 10, Title: "Story10"},
		}

		expectedResponse := "Story Id: 1, Story Title: Story1\nStory Id: 2, Story Title: Story2\nStory Id: 10, Story Title: Story10\n"

		messagePromptResponse := ConstructPromptMessageAttribute(storiesParam)

		if messagePromptResponse != expectedResponse {
			t.Fatalf("expected message prompt to be %s, got %s", expectedResponse, messagePromptResponse)
		}

	})

}

func TestClassifyTechNewsStory(t *testing.T) {

	t.Run("calls constructPromptSystemAttribute once", func(t *testing.T) {
		callCount := 0
		constructPromptSystemAttribute = func() string {
			callCount++
			return "system prompt"
		}
		defer func() { constructPromptSystemAttribute = ConstructPromptSystemAttribute }()

		constructPromptMessageAttribute = func(stories []StoryDetail) string { return "message prompt" }
		defer func() { constructPromptMessageAttribute = ConstructPromptMessageAttribute }()

		claudeMessageApiCall = func(prompt PromptInput, classificationResponse *ClassificationResponse) error {
			classificationResponse.Content = []ResponseContent{{Type: "text", Text: "[]"}}
			return nil
		}
		defer func() { claudeMessageApiCall = ClaudeMessageApiCall }()

		ClassifyTechNewsStory([]StoryDetail{{Id: 1, Title: "Story1"}})

		if callCount != 1 {
			t.Fatalf("expected constructPromptSystemAttribute to be called once, got %d", callCount)
		}
	})

	t.Run("calls constructPromptMessageAttribute with all stories", func(t *testing.T) {
		constructPromptSystemAttribute = func() string { return "system prompt" }
		defer func() { constructPromptSystemAttribute = ConstructPromptSystemAttribute }()

		var calledWith []StoryDetail
		constructPromptMessageAttribute = func(stories []StoryDetail) string {
			calledWith = stories
			return "message prompt"
		}
		defer func() { constructPromptMessageAttribute = ConstructPromptMessageAttribute }()

		claudeMessageApiCall = func(prompt PromptInput, classificationResponse *ClassificationResponse) error {
			classificationResponse.Content = []ResponseContent{{Type: "text", Text: "[]"}}
			return nil
		}
		defer func() { claudeMessageApiCall = ClaudeMessageApiCall }()

		input := []StoryDetail{{Id: 1, Title: "Story1"}, {Id: 2, Title: "Story2"}, {Id: 3, Title: "Story3"}, {Id: 10, Title: "Story10"}}
		ClassifyTechNewsStory(input)

		if len(calledWith) != len(input) {
			t.Fatalf("expected constructPromptMessageAttribute called with %d stories, got %d", len(input), len(calledWith))
		}
	})

	t.Run("calls claudeMessageApiCall with composed prompt", func(t *testing.T) {
		constructPromptSystemAttribute = func() string { return "system prompt" }
		defer func() { constructPromptSystemAttribute = ConstructPromptSystemAttribute }()

		constructPromptMessageAttribute = func(stories []StoryDetail) string { return "message prompt" }
		defer func() { constructPromptMessageAttribute = ConstructPromptMessageAttribute }()

		var calledWith PromptInput
		claudeMessageApiCall = func(prompt PromptInput, classificationResponse *ClassificationResponse) error {
			calledWith = prompt
			classificationResponse.Content = []ResponseContent{{Type: "text", Text: "[]"}}
			return nil
		}
		defer func() { claudeMessageApiCall = ClaudeMessageApiCall }()

		ClassifyTechNewsStory([]StoryDetail{{Id: 1, Title: "Story1"}})

		expected := PromptInput{System: "system prompt", Message: "message prompt"}
		if calledWith != expected {
			t.Fatalf("expected claudeMessageApiCall called with %v, got %v", expected, calledWith)
		}
	})

	t.Run("returns classified story IDs from Claude response", func(t *testing.T) {
		constructPromptSystemAttribute = func() string { return "system prompt" }
		defer func() { constructPromptSystemAttribute = ConstructPromptSystemAttribute }()

		constructPromptMessageAttribute = func(stories []StoryDetail) string { return "message prompt" }
		defer func() { constructPromptMessageAttribute = ConstructPromptMessageAttribute }()

		claudeMessageApiCall = func(prompt PromptInput, classificationResponse *ClassificationResponse) error {
			classificationResponse.Content = []ResponseContent{{Type: "text", Text: "[1,3]"}}
			return nil
		}
		defer func() { claudeMessageApiCall = ClaudeMessageApiCall }()

		result := ClassifyTechNewsStory([]StoryDetail{{Id: 1, Title: "Story1"}, {Id: 2, Title: "Story2"}, {Id: 3, Title: "Story3"}})

		if len(result) != 2 || result[0] != 1 || result[1] != 3 {
			t.Fatalf("expected [1 3], got %v", result)
		}
	})

	t.Run("returns empty slice when Claude returns empty content", func(t *testing.T) {
		claudeMessageApiCall = func(prompt PromptInput, classificationResponse *ClassificationResponse) error {
			classificationResponse.Content = []ResponseContent{}
			return nil
		}
		defer func() { claudeMessageApiCall = ClaudeMessageApiCall }()

		result := ClassifyTechNewsStory([]StoryDetail{{Id: 1, Title: "Story1"}})

		if len(result) != 0 {
			t.Fatalf("expected empty slice, got %v", result)
		}
	})

}
