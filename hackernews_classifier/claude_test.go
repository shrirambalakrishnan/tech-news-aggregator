package hackernews_classifier

import (
	"strings"
	"testing"

	"github.com/shrirambalakrishnan/tech-news/claudeapi"
)

func TestUserProfileIsEmpty(t *testing.T) {
	t.Run("true when no summary and no interests", func(t *testing.T) {
		if !(UserProfile{}).IsEmpty() {
			t.Fatalf("expected empty UserProfile to report IsEmpty()")
		}
	})

	t.Run("false when summary is set", func(t *testing.T) {
		if (UserProfile{Summary: "backend work"}).IsEmpty() {
			t.Fatalf("expected UserProfile with a summary to not be empty")
		}
	})

	t.Run("false when interests are set", func(t *testing.T) {
		if (UserProfile{Interests: []string{"Go"}}).IsEmpty() {
			t.Fatalf("expected UserProfile with interests to not be empty")
		}
	})

	t.Run("false when retrieved excerpts are set", func(t *testing.T) {
		if (UserProfile{RetrievedExcerpts: []string{"a retrieved chunk"}}).IsEmpty() {
			t.Fatalf("expected UserProfile with retrieved excerpts to not be empty")
		}
	})
}

func TestConstructPromptSystemAttribute(t *testing.T) {

	t.Run("returns the static prompt for ArmGeneric", func(t *testing.T) {

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

		systemPromptResponse := ConstructPromptSystemAttribute(ArmGeneric, UserProfile{})

		if systemPromptResponse != expectedResponse {
			t.Fatalf("expected system prompt to be %s, got %s", expectedResponse, systemPromptResponse)
		}
	})

	t.Run("injects the profile and drops the static rules for ArmInterests", func(t *testing.T) {
		profile := UserProfile{
			Summary:   "Repositories focus on distributed systems and clock ordering.",
			Interests: []string{"distributed systems", "Spanner", "message brokers"},
		}

		got := ConstructPromptSystemAttribute(ArmInterests, profile)

		if !strings.Contains(got, profile.Summary) {
			t.Fatalf("expected dynamic prompt to contain the summary, got %s", got)
		}
		for _, interest := range profile.Interests {
			if !strings.Contains(got, interest) {
				t.Fatalf("expected dynamic prompt to contain interest %q, got %s", interest, got)
			}
		}
		if strings.Contains(got, "Programming languages, compilers, interpreters") {
			t.Fatalf("expected dynamic prompt to omit the static ruleset, got %s", got)
		}
	})

	t.Run("builds the prompt from retrieved excerpts alone for ArmRAG", func(t *testing.T) {
		profile := UserProfile{
			Summary:           "Repositories focus on distributed systems.",
			Interests:         []string{"Spanner"},
			RetrievedExcerpts: []string{"TrueTime bounds clock uncertainty", "Raft elects a single leader"},
		}

		got := ConstructPromptSystemAttribute(ArmRAG, profile)

		for i, chunk := range profile.RetrievedExcerpts {
			if !strings.Contains(got, chunk) {
				t.Fatalf("expected rag prompt to contain chunk %d, got %s", i+1, got)
			}
		}
		if strings.Contains(got, profile.Summary) {
			t.Fatalf("expected rag prompt to omit the profile summary (arms stay pure), got %s", got)
		}
		if strings.Contains(got, "Programming languages, compilers, interpreters") {
			t.Fatalf("expected rag prompt to omit the static ruleset, got %s", got)
		}
	})

	// The arm - not the presence of RetrievedExcerpts - selects the flow, so a
	// profile carrying both must still yield exactly its arm's prompt.
	t.Run("ignores retrieved excerpts for ArmInterests", func(t *testing.T) {
		profile := UserProfile{
			Summary:           "Repositories focus on distributed systems.",
			Interests:         []string{"Spanner"},
			RetrievedExcerpts: []string{"TrueTime bounds clock uncertainty"},
		}

		got := ConstructPromptSystemAttribute(ArmInterests, profile)

		if strings.Contains(got, profile.RetrievedExcerpts[0]) {
			t.Fatalf("expected the interests prompt to omit retrieved excerpts, got %s", got)
		}
		if !strings.Contains(got, profile.Summary) {
			t.Fatalf("expected the interests prompt to contain the summary, got %s", got)
		}
	})

	t.Run("ignores retrieved excerpts for ArmGeneric", func(t *testing.T) {
		profile := UserProfile{RetrievedExcerpts: []string{"TrueTime bounds clock uncertainty"}}

		got := ConstructPromptSystemAttribute(ArmGeneric, profile)

		if strings.Contains(got, profile.RetrievedExcerpts[0]) {
			t.Fatalf("expected the static prompt to omit retrieved excerpts, got %s", got)
		}
		if !strings.Contains(got, "Programming languages, compilers, interpreters") {
			t.Fatalf("expected the static ruleset, got %s", got)
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
		constructPromptSystemAttribute = func(_ Arm, profile UserProfile) string {
			callCount++
			return "system prompt"
		}
		defer func() { constructPromptSystemAttribute = ConstructPromptSystemAttribute }()

		constructPromptMessageAttribute = func(stories []StoryDetail) string { return "message prompt" }
		defer func() { constructPromptMessageAttribute = ConstructPromptMessageAttribute }()

		claudeMessageApiCall = func(prompt claudeapi.PromptInput, response *claudeapi.Response) error {
			response.Content = []claudeapi.ResponseContent{{Type: "text", Text: "[]"}}
			return nil
		}
		defer func() { claudeMessageApiCall = claudeapi.ClaudeMessageApiCall }()

		ClassifyTechNewsStory(ArmGeneric, []StoryDetail{{Id: 1, Title: "Story1"}}, UserProfile{})

		if callCount != 1 {
			t.Fatalf("expected constructPromptSystemAttribute to be called once, got %d", callCount)
		}
	})

	t.Run("calls constructPromptMessageAttribute with all stories", func(t *testing.T) {
		constructPromptSystemAttribute = func(_ Arm, profile UserProfile) string { return "system prompt" }
		defer func() { constructPromptSystemAttribute = ConstructPromptSystemAttribute }()

		var calledWith []StoryDetail
		constructPromptMessageAttribute = func(stories []StoryDetail) string {
			calledWith = stories
			return "message prompt"
		}
		defer func() { constructPromptMessageAttribute = ConstructPromptMessageAttribute }()

		claudeMessageApiCall = func(prompt claudeapi.PromptInput, response *claudeapi.Response) error {
			response.Content = []claudeapi.ResponseContent{{Type: "text", Text: "[]"}}
			return nil
		}
		defer func() { claudeMessageApiCall = claudeapi.ClaudeMessageApiCall }()

		input := []StoryDetail{{Id: 1, Title: "Story1"}, {Id: 2, Title: "Story2"}, {Id: 3, Title: "Story3"}, {Id: 10, Title: "Story10"}}
		ClassifyTechNewsStory(ArmGeneric, input, UserProfile{})

		if len(calledWith) != len(input) {
			t.Fatalf("expected constructPromptMessageAttribute called with %d stories, got %d", len(input), len(calledWith))
		}
	})

	t.Run("calls claudeMessageApiCall with composed prompt", func(t *testing.T) {
		constructPromptSystemAttribute = func(_ Arm, profile UserProfile) string { return "system prompt" }
		defer func() { constructPromptSystemAttribute = ConstructPromptSystemAttribute }()

		constructPromptMessageAttribute = func(stories []StoryDetail) string { return "message prompt" }
		defer func() { constructPromptMessageAttribute = ConstructPromptMessageAttribute }()

		var calledWith claudeapi.PromptInput
		claudeMessageApiCall = func(prompt claudeapi.PromptInput, response *claudeapi.Response) error {
			calledWith = prompt
			response.Content = []claudeapi.ResponseContent{{Type: "text", Text: "[]"}}
			return nil
		}
		defer func() { claudeMessageApiCall = claudeapi.ClaudeMessageApiCall }()

		ClassifyTechNewsStory(ArmGeneric, []StoryDetail{{Id: 1, Title: "Story1"}}, UserProfile{})

		expected := claudeapi.PromptInput{System: "system prompt", Message: "message prompt"}
		if calledWith != expected {
			t.Fatalf("expected claudeMessageApiCall called with %v, got %v", expected, calledWith)
		}
	})

	t.Run("returns classified story IDs from Claude response", func(t *testing.T) {
		constructPromptSystemAttribute = func(_ Arm, profile UserProfile) string { return "system prompt" }
		defer func() { constructPromptSystemAttribute = ConstructPromptSystemAttribute }()

		constructPromptMessageAttribute = func(stories []StoryDetail) string { return "message prompt" }
		defer func() { constructPromptMessageAttribute = ConstructPromptMessageAttribute }()

		claudeMessageApiCall = func(prompt claudeapi.PromptInput, response *claudeapi.Response) error {
			response.Content = []claudeapi.ResponseContent{{Type: "text", Text: "[1,3]"}}
			return nil
		}
		defer func() { claudeMessageApiCall = claudeapi.ClaudeMessageApiCall }()

		result := ClassifyTechNewsStory(ArmGeneric, []StoryDetail{{Id: 1, Title: "Story1"}, {Id: 2, Title: "Story2"}, {Id: 3, Title: "Story3"}}, UserProfile{})

		if len(result) != 2 || result[0] != 1 || result[1] != 3 {
			t.Fatalf("expected [1 3], got %v", result)
		}
	})

	t.Run("returns empty slice when Claude returns empty content", func(t *testing.T) {
		claudeMessageApiCall = func(prompt claudeapi.PromptInput, response *claudeapi.Response) error {
			response.Content = []claudeapi.ResponseContent{}
			return nil
		}
		defer func() { claudeMessageApiCall = claudeapi.ClaudeMessageApiCall }()

		result := ClassifyTechNewsStory(ArmGeneric, []StoryDetail{{Id: 1, Title: "Story1"}}, UserProfile{})

		if len(result) != 0 {
			t.Fatalf("expected empty slice, got %v", result)
		}
	})

	t.Run("forwards the user profile to constructPromptSystemAttribute", func(t *testing.T) {
		var calledWith UserProfile
		constructPromptSystemAttribute = func(_ Arm, profile UserProfile) string {
			calledWith = profile
			return "system prompt"
		}
		defer func() { constructPromptSystemAttribute = ConstructPromptSystemAttribute }()

		claudeMessageApiCall = func(prompt claudeapi.PromptInput, response *claudeapi.Response) error {
			response.Content = []claudeapi.ResponseContent{{Type: "text", Text: "[]"}}
			return nil
		}
		defer func() { claudeMessageApiCall = claudeapi.ClaudeMessageApiCall }()

		want := UserProfile{Summary: "backend work", Interests: []string{"Go", "Spanner"}}
		ClassifyTechNewsStory(ArmGeneric, []StoryDetail{{Id: 1, Title: "Story1"}}, want)

		if calledWith.Summary != want.Summary || len(calledWith.Interests) != len(want.Interests) {
			t.Fatalf("expected profile %+v forwarded, got %+v", want, calledWith)
		}
	})

}

// TestExtractJSONArray covers the wrappers Claude actually emits despite the
// prompt forbidding them. The fenced case is not hypothetical: it is what broke
// an arm 2 run (the RAG prompt inlines fenced corpus excerpts, and the model
// mirrors that formatting back).
func TestExtractJSONArray(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare array", "[1, 2, 3]", "[1, 2, 3]"},
		{"json fence", "```json\n[49112232, 49111176]\n```", "[49112232, 49111176]"},
		{"bare fence", "```\n[1,2]\n```", "[1,2]"},
		{"prose preamble", "Here are the relevant story IDs:\n[7, 8]", "[7, 8]"},
		{"trailing commentary", "[7, 8]\nThese match the reader's interests.", "[7, 8]"},
		{"empty array", "```json\n[]\n```", "[]"},
		{"surrounding whitespace", "\n\n  [1]  \n", "[1]"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractJSONArray(c.in); got != c.want {
				t.Errorf("extractJSONArray(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}

	// No array at all -> "", which fails the caller's Unmarshal exactly as an
	// unparseable response did before. Silently succeeding with no IDs would be
	// indistinguishable from "nothing was relevant".
	t.Run("no array present", func(t *testing.T) {
		for _, in := range []string{"", "I could not classify these stories.", "[1, 2"} {
			if got := extractJSONArray(in); got != "" {
				t.Errorf("extractJSONArray(%q) = %q, want \"\"", in, got)
			}
		}
	})
}

// A fenced response must reach the caller as IDs, not as the empty slice a parse
// failure produces - in the eval those two outcomes look identical (recall 0).
func TestClassifyTechNewsStoryParsesFencedResponse(t *testing.T) {
	claudeMessageApiCall = func(prompt claudeapi.PromptInput, response *claudeapi.Response) error {
		response.Content = []claudeapi.ResponseContent{{Type: "text", Text: "```json\n[49112232, 49111176]\n```"}}
		return nil
	}
	defer func() { claudeMessageApiCall = claudeapi.ClaudeMessageApiCall }()

	ids := ClassifyTechNewsStory(ArmRAG, []StoryDetail{{Id: 1, Title: "story1"}}, UserProfile{RetrievedExcerpts: []string{"chunk"}})

	want := []int{49112232, 49111176}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i, id := range want {
		if ids[i] != id {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
}
