package profile

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/shrirambalakrishnan/tech-news/claudeapi"
)

const (
	USER_CONTEXT_FILE           = "profile/user_context.json"
	USER_CONTEXT_SCHEMA_VERSION = 1
)

// UserContext is the artifact written to profile/user_context.json by the
// prebuild step and read by the main run. It is a regenerable cache, not a
// source of truth: extraction is expensive (N GitHub calls + 1 LLM call) and
// rarely changes, while the classify run happens every 4 hours.
//
// The shape is a hybrid: a prose Summary for Claude to reason and generalize
// over, plus a flat Interests array that stays inspectable/diffable. The
// metadata fields (SchemaVersion, GeneratedAt, Source, Model) record provenance
// so the file is reproducible and forward-compatible.
type UserContext struct {
	SchemaVersion int      `json:"schema_version"`
	GeneratedAt   string   `json:"generated_at"`
	Source        string   `json:"source"`
	Model         string   `json:"model"`
	Summary       string   `json:"summary"`
	Interests     []string `json:"interests"`
}

// extractedContext is the raw shape we ask Claude to return — just the derived
// fields. Metadata is added on our side, never trusted from the model.
type extractedContext struct {
	Summary   string   `json:"summary"`
	Interests []string `json:"interests"`
}

var constructUserContextSystemAttribute = ConstructUserContextSystemAttribute
var constructUserContextMessageAttribute = ConstructUserContextMessageAttribute
var claudeMessageApiCall = claudeapi.ClaudeMessageApiCall

func ConstructUserContextSystemAttribute() string {
	return `You build a concise technical interest profile for a software developer from the README files of their GitHub repositories.

You will receive the README contents of several repositories. Identify the technical topics and domains they actually cover.

Return a JSON object with exactly two fields:
- "summary": a 1-3 sentence description of the technical domains and topics evident in the repositories. Do NOT assign the developer a role, title, or seniority (e.g. "full-stack", "backend", "senior"), and do NOT claim skills or breadth not directly shown. Describe the work, not the person.
- "interests": a flat array of 10-30 specific technical interest keywords, scaled to how diverse the repositories are. Keywords must be specific enough to match technical news headlines (e.g. "WebAssembly runtimes", "Rust", "distributed systems") - not vague terms like "programming" or "software".

Ground every statement only in what is explicitly present in the READMEs. Do not generalize beyond the provided repositories or infer adjacent skills - if the repositories only cover backend topics, do not mention frontend or full-stack. A technology being used in a project (e.g. a web framework) does not by itself imply a broader role. Ignore boilerplate such as installation steps, badges, license text, and contribution guidelines.

Return ONLY the raw JSON object. No explanation, no markdown fences, no wrapping.
Example response: {"summary": "Repositories focus on distributed systems integration and event-driven APIs, covering message brokers and clock/causality ordering.", "interests": ["distributed systems", "event-driven architecture", "message brokers", "API integrations"]}
`
}

// stripJSONFences removes a surrounding Markdown code fence (```json ... ```)
// if the model wrapped its response in one despite being asked not to.
func stripJSONFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening fence line (``` or ```json).
	if i := strings.IndexByte(s, '\n'); i != -1 {
		s = s[i+1:]
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func ConstructUserContextMessageAttribute(readmes []string) string {
	var b strings.Builder
	for i, readme := range readmes {
		fmt.Fprintf(&b, "===== Repository README %d =====\n%s\n\n", i+1, readme)
	}
	return b.String()
}

// ExtractUserContext sends all READMEs in a single LLM call and returns a
// UserContext wrapped with provenance metadata. githubUsername is recorded as
// the source.
func ExtractUserContext(readmes []string, githubUsername string) (UserContext, error) {
	var prompt claudeapi.PromptInput
	prompt.System = constructUserContextSystemAttribute()
	prompt.Message = constructUserContextMessageAttribute(readmes)

	var response claudeapi.Response
	if err := claudeMessageApiCall(prompt, &response); err != nil {
		return UserContext{}, fmt.Errorf("claude api call failed: %w", err)
	}

	if len(response.Content) == 0 {
		return UserContext{}, fmt.Errorf("empty content in user context response")
	}

	raw := response.Content[0].Text
	log.Println("user context raw response = ", raw)

	var extracted extractedContext
	if err := json.Unmarshal([]byte(stripJSONFences(raw)), &extracted); err != nil {
		return UserContext{}, fmt.Errorf("failed to unmarshal extracted context: %w", err)
	}

	return UserContext{
		SchemaVersion: USER_CONTEXT_SCHEMA_VERSION,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Source:        "github:" + githubUsername,
		Model:         claudeapi.ANTHROPIC_MODEL_NAME,
		Summary:       extracted.Summary,
		Interests:     extracted.Interests,
	}, nil
}

// WriteUserContext persists the user context as indented JSON.
func WriteUserContext(userContext UserContext, path string) error {
	data, err := json.MarshalIndent(userContext, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal user context: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write user context to %s: %w", path, err)
	}
	return nil
}

// LoadUserContext reads the user context from disk. The main run uses this on
// each run; callers should fail soft (fall back to the static rules) when it
// returns an error, since the prebuild step may not have run yet.
func LoadUserContext(path string) (UserContext, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return UserContext{}, fmt.Errorf("failed to read user context from %s: %w", path, err)
	}
	var userContext UserContext
	if err := json.Unmarshal(data, &userContext); err != nil {
		return UserContext{}, fmt.Errorf("failed to unmarshal user context: %w", err)
	}
	return userContext, nil
}
