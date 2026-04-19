package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

func PostJSON(url string, headers map[string]string, body any, result any) error {
	jsonData, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal error: %w", err)
	}

	requestBody := bytes.NewBuffer(jsonData)
	req, err := http.NewRequest("POST", url, requestBody)
	if err != nil {
		return fmt.Errorf("request error: %w", err)
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	client := &http.Client{}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post error: %w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("non-200 response: %s", resp.Status)
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode error: %w", err)
		}
	}

	return nil
}
