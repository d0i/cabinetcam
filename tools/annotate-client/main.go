// annotate-client: A local client for annotating CabinetCam boxes using Ollama vision models.
//
// This client runs on your Mac (or any machine with Ollama installed).
// It fetches the next unannotated box from the CabinetCam server,
// downloads the photo, sends it to a local Ollama instance for description,
// and posts the annotation back to the server.
//
// Usage:
//
//	./annotate-client \
//	  -server https://stone-finder.exe.xyz:8000 \
//	  -token <your-api-token> \
//	  -ollama http://127.0.0.1:11434 \
//	  -model llava \
//	  -loop
//
// Flags:
//
//	-server   CabinetCam server URL (required)
//	-token    API bearer token (required; create via POST /api/tokens)
//	-ollama   Ollama API base URL (default: http://127.0.0.1:11434)
//	-model    Ollama vision model name (default: llava)
//	-prompt   Custom prompt for the vision model
//	-loop     Keep running until all boxes are annotated
//	-dry-run  Fetch and describe but don't submit annotations
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// CabinetCam API types
type AnnotateNextResponse struct {
	BoxID                 string `json:"box_id"`
	BoxName               string `json:"box_name"`
	PhotoID               string `json:"photo_id"`
	PhotoURL              string `json:"photo_url"`
	CurrentAnnotation     string `json:"current_annotation"`
	PhotoCount            int    `json:"photo_count"`
	PhotosSinceAnnotation int    `json:"photos_since_annotation"`
	Reason                string `json:"reason"`
}

// Ollama API types
type OllamaMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

type OllamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []OllamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

type OllamaChatResponse struct {
	Model   string        `json:"model"`
	Message OllamaMessage `json:"message"`
	Done    bool          `json:"done"`
}

func main() {
	serverURL := flag.String("server", "", "CabinetCam server URL (e.g. https://stone-finder.exe.xyz:8000)")
	token := flag.String("token", "", "API bearer token")
	ollamaURL := flag.String("ollama", "http://127.0.0.1:11434", "Ollama API base URL")
	model := flag.String("model", "llava", "Ollama vision model name")
	prompt := flag.String("prompt", "Describe the contents of this cabinet or box photo concisely. List the items you can see. Be specific about quantities and types where possible. Respond with only the item list, no preamble.", "Prompt for vision model")
	loop := flag.Bool("loop", false, "Keep running until all boxes are annotated")
	dryRun := flag.Bool("dry-run", false, "Fetch and describe but don't submit")
	flag.Parse()

	if *serverURL == "" || *token == "" {
		fmt.Fprintf(os.Stderr, "Usage: annotate-client -server <url> -token <token> [options]\n\n")
		fmt.Fprintf(os.Stderr, "Required:\n")
		fmt.Fprintf(os.Stderr, "  -server  CabinetCam server URL\n")
		fmt.Fprintf(os.Stderr, "  -token   API bearer token (create at server /settings page)\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	*serverURL = strings.TrimRight(*serverURL, "/")
	*ollamaURL = strings.TrimRight(*ollamaURL, "/")

	client := &http.Client{Timeout: 120 * time.Second}

	// Check Ollama connectivity
	log.Println("Checking Ollama connectivity...")
	if err := checkOllama(client, *ollamaURL); err != nil {
		log.Fatalf("Cannot connect to Ollama at %s: %v", *ollamaURL, err)
	}
	log.Printf("Ollama OK at %s", *ollamaURL)

	// Check CabinetCam connectivity
	log.Println("Checking CabinetCam server connectivity...")
	if err := checkServer(client, *serverURL, *token); err != nil {
		log.Fatalf("Cannot connect to CabinetCam at %s: %v", *serverURL, err)
	}
	log.Printf("CabinetCam OK at %s", *serverURL)

	for {
		annotated, err := processNext(client, *serverURL, *token, *ollamaURL, *model, *prompt, *dryRun)
		if err != nil {
			log.Printf("Error: %v", err)
			if *loop {
				log.Println("Retrying in 5 seconds...")
				time.Sleep(5 * time.Second)
				continue
			}
			os.Exit(1)
		}
		if !annotated {
			log.Println("All boxes are annotated. Nothing to do.")
			break
		}
		if !*loop {
			break
		}
		log.Println("---")
	}
}

func checkOllama(client *http.Client, ollamaURL string) error {
	resp, err := client.Get(ollamaURL + "/api/tags")
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

func checkServer(client *http.Client, serverURL, token string) error {
	req, _ := http.NewRequest("GET", serverURL+"/api/annotate/next", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return fmt.Errorf("authentication failed; check your token")
	}
	// 200 or 204 are both fine
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

func processNext(client *http.Client, serverURL, token, ollamaURL, model, prompt string, dryRun bool) (bool, error) {
	// Step 1: Get next box to annotate
	log.Println("Fetching next box to annotate...")
	req, _ := http.NewRequest("GET", serverURL+"/api/annotate/next", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("fetch next: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 204 {
		return false, nil
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("fetch next: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var box AnnotateNextResponse
	if err := json.NewDecoder(resp.Body).Decode(&box); err != nil {
		return false, fmt.Errorf("decode response: %w", err)
	}

	log.Printf("Box: %s (%s)", box.BoxName, box.BoxID)
	log.Printf("  Reason: %s, Photos: %d, New since annotation: %d", box.Reason, box.PhotoCount, box.PhotosSinceAnnotation)
	if box.CurrentAnnotation != "" {
		log.Printf("  Current annotation: %s", box.CurrentAnnotation)
	}

	// Step 2: Download the photo
	log.Printf("Downloading photo %s...", box.PhotoID)
	req, _ = http.NewRequest("GET", serverURL+box.PhotoURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	if err != nil {
		return false, fmt.Errorf("download photo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return false, fmt.Errorf("download photo: HTTP %d", resp.StatusCode)
	}

	imageData, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("read photo: %w", err)
	}
	log.Printf("  Photo size: %d bytes", len(imageData))

	// Step 3: Send to Ollama for annotation
	log.Printf("Sending to Ollama (%s model: %s)...", ollamaURL, model)
	imageB64 := base64.StdEncoding.EncodeToString(imageData)

	ollamaReq := OllamaChatRequest{
		Model: model,
		Messages: []OllamaMessage{
			{
				Role:    "user",
				Content: prompt,
				Images:  []string{imageB64},
			},
		},
		Stream: false,
	}

	reqBody, _ := json.Marshal(ollamaReq)
	start := time.Now()
	resp, err = client.Post(ollamaURL+"/api/chat", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return false, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("ollama: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var ollamaResp OllamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return false, fmt.Errorf("decode ollama response: %w", err)
	}

	annotation := strings.TrimSpace(ollamaResp.Message.Content)
	log.Printf("  Ollama response (%.1fs): %s", time.Since(start).Seconds(), annotation)

	if dryRun {
		log.Println("  [DRY RUN] Skipping submission")
		return true, nil
	}

	// Step 4: Ask for confirmation
	fmt.Printf("\n  Submit this annotation? [y/N] ")
	var answer string
	fmt.Scanln(&answer)
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		log.Println("  ⏭️  Skipped by user")
		return true, nil
	}

	// Step 5: Submit annotation back to CabinetCam
	log.Printf("Submitting annotation for box %s...", box.BoxID)
	submitBody, _ := json.Marshal(map[string]string{
		"annotation": annotation,
		"photo_id":   box.PhotoID,
	})

	req, _ = http.NewRequest("POST", serverURL+"/api/annotate/"+box.BoxID, bytes.NewReader(submitBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		return false, fmt.Errorf("submit annotation: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("submit annotation: HTTP %d: %s", resp.StatusCode, string(body))
	}

	log.Printf("  ✅ Annotated \"%s\" successfully", box.BoxName)
	return true, nil
}
