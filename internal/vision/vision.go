package vision

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
)

const model = "gemma4:e2b"

const promptImageOnly = `You are helping a deaf person know who is at their door.
Describe who or what you see in one or two sentences.
Be specific: mention clothing, approximate age, what they are carrying.
Example: "A young man in a blue shirt is holding a package at the door."`

const promptImageAndAudio = `You are helping a deaf person know who is at their door.
First describe who or what you see (clothing, approximate age, what they are carrying).
Then transcribe anything being said.
Example: "A delivery man in a brown uniform is at the door carrying a package. He is saying: 'I have a delivery for you.'"`

// Client calls Ollama's local API to describe an image.
type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{baseURL: baseURL, http: &http.Client{}}
}

type ollamaRequest struct {
	Model  string   `json:"model"`
	Prompt string   `json:"prompt"`
	Images []string `json:"images"`
	Stream bool     `json:"stream"`
}

type ollamaResponse struct {
	Response string `json:"response"`
}

// Describe sends an image to Gemma 4 and returns a natural language description.
func (c *Client) Describe(img []byte) (string, error) {
	return c.DescribeWithAudio(img, nil)
}

// DescribeWithAudio sends an image and optional audio to Gemma 4.
// Pass nil for audio to describe image only.
// NOTE: Ollama audio support for Gemma 4 E2B is unverified — test after model pull.
func (c *Client) DescribeWithAudio(img []byte, audioWAV []byte) (string, error) {
	p := promptImageOnly
	images := []string{base64.StdEncoding.EncodeToString(img)}

	if len(audioWAV) > 0 {
		p = promptImageAndAudio
		// Audio passed as second base64 entry — verify Ollama supports this for Gemma 4.
		images = append(images, base64.StdEncoding.EncodeToString(audioWAV))
	}

	body := ollamaRequest{
		Model:  model,
		Prompt: p,
		Images: images,
		Stream: false,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	resp, err := c.http.Post(c.baseURL+"/api/generate", "application/json", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama returned %d", resp.StatusCode)
	}

	var result ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	return result.Response, nil
}
