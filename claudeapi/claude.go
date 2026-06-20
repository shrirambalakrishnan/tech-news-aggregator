package claudeapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

const (
	ANTHROPIC_API_HOST         = "https://api.anthropic.com/"
	ANTHROPIC_API_VERSION      = "v1"
	ANTHROPIC_MESSAGE_ENDPOINT = "messages"
	ANTHROPIC_MESSAGE_URL      = ANTHROPIC_API_HOST + ANTHROPIC_API_VERSION + "/" + ANTHROPIC_MESSAGE_ENDPOINT
	ANTHROPIC_MODEL_NAME       = "claude-haiku-4-5-20251001"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system"`
	Messages  []Message `json:"messages"`
}

type ResponseContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type Response struct {
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

func ClaudeMessageApiCall(prompt PromptInput, response *Response) error {
	message := Message{
		Role:    "user",
		Content: prompt.Message,
	}
	reqBody := Request{
		Model:     ANTHROPIC_MODEL_NAME,
		MaxTokens: 1024,
		System:    prompt.System,
		Messages:  []Message{message},
	}
	log.Println("reqBody = ", reqBody)

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal error: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, ANTHROPIC_MESSAGE_URL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("request error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", os.Getenv("ANTHROPIC_API_KEY"))
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %s: %s", resp.Status, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(response); err != nil {
		return fmt.Errorf("decode error: %w", err)
	}

	return nil
}
