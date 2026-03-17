package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// Request/Response structures matching Ollama API
type Message struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type ChatResponse struct {
	Model         string  `json:"model"`
	CreatedAt     string  `json:"created_at"`
	Message       Message `json:"message"`
	Done          bool    `json:"done"`
	TotalDuration int64   `json:"total_duration"`
	EvalCount     int     `json:"eval_count"`
}

type ModelInfo struct {
	Name       string `json:"name"`
	ModifiedAt string `json:"modified_at"`
	Size       int64  `json:"size"`
}

type TagsResponse struct {
	Models []ModelInfo `json:"models"`
}

// Predefined lists for generating realistic annotations
var (
	householdItems = []string{
		"ceramic plates", "coffee mug", "water bottle", "glass jar",
		"wooden spoon", "metal utensils", "plastic container", "paper towels",
		"dish soap", "sponge", "cutting board", "kitchen towel",
		"bowl", "cup", "fork", "knife", "spoon", "tupperware",
		"food storage bag", "aluminum foil", "plastic wrap", "napkins",
		"salt shaker", "pepper mill", "measuring cup", "spatula",
		"whisk", "can opener", "bottle opener", "strainer",
	}

	quantities = []string{
		"a", "two", "three", "a few", "several", "a couple of",
		"1", "2", "3", "4", "5",
	}

	adjectives = []string{
		"white", "blue", "red", "green", "black", "yellow",
		"small", "large", "medium", "tall", "short",
		"clean", "empty", "full", "new", "old",
		"plastic", "metal", "wooden", "ceramic", "glass",
		"colorful", "transparent", "opaque", "shiny", "matte",
	}

	containers = []string{
		"box", "basket", "bin", "drawer", "shelf",
		"cabinet", "bag", "tray", "rack", "holder",
	}
)

// generateAnnotation creates a deterministic fake annotation based on image hash
func generateAnnotation(imageData []byte) string {
	// Compute SHA-256 hash
	hash := sha256.Sum256(imageData)
	
	// Use hash bytes to deterministically select items
	var items []string
	numItems := int(hash[0]%5) + 2 // 2-6 items
	
	for i := 0; i < numItems; i++ {
		idx := int(hash[i*2]) % len(householdItems)
		qtyIdx := int(hash[i*2+1]) % len(quantities)
		adjIdx := int(hash[i*2+2]) % len(adjectives)
		
		quantity := quantities[qtyIdx]
		adjective := adjectives[adjIdx]
		item := householdItems[idx]
		
		// Sometimes skip adjective for variety
		if hash[i*3]%3 == 0 {
			items = append(items, fmt.Sprintf("%s %s", quantity, item))
		} else {
			items = append(items, fmt.Sprintf("%s %s %s", quantity, adjective, item))
		}
	}
	
	// Add optional container at the end
	if hash[31]%2 == 0 {
		containerIdx := int(hash[30]) % len(containers)
		adjIdx := int(hash[29]) % len(adjectives)
		items = append(items, fmt.Sprintf("a %s %s", adjectives[adjIdx], containers[containerIdx]))
	}
	
	// Join items into a natural sentence
	if len(items) == 0 {
		return "An empty cabinet shelf."
	}
	
	if len(items) == 1 {
		return fmt.Sprintf("I can see %s.", items[0])
	}
	
	// Join with commas and "and" for the last item
	result := "I can see " + strings.Join(items[:len(items)-1], ", ")
	result += ", and " + items[len(items)-1] + "."
	
	return result
}

// handleChat processes POST /api/chat requests
func handleChat(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	log.Printf("[%s] %s %s", r.Method, r.URL.Path, r.RemoteAddr)
	
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("Error decoding request: %v", err)
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	
	log.Printf("  Model: %s, Messages: %d, Stream: %v", req.Model, len(req.Messages), req.Stream)
	
	// Extract first image from messages
	var imageData []byte
	for _, msg := range req.Messages {
		if len(msg.Images) > 0 {
			// Decode base64 image
			var err error
			imageData, err = base64.StdEncoding.DecodeString(msg.Images[0])
			if err != nil {
				log.Printf("Error decoding image: %v", err)
				http.Error(w, "Invalid image data", http.StatusBadRequest)
				return
			}
			log.Printf("  Image size: %d bytes", len(imageData))
			break
		}
	}
	
	if imageData == nil {
		log.Printf("  No image found in request")
		http.Error(w, "No image provided", http.StatusBadRequest)
		return
	}
	
	// Simulate processing time
	time.Sleep(500 * time.Millisecond)
	
	// Generate deterministic annotation
	annotation := generateAnnotation(imageData)
	log.Printf("  Generated annotation: %s", annotation)
	
	// Build response
	response := ChatResponse{
		Model:     req.Model,
		CreatedAt: time.Now().Format(time.RFC3339),
		Message: Message{
			Role:    "assistant",
			Content: annotation,
		},
		Done:          true,
		TotalDuration: time.Since(start).Nanoseconds(),
		EvalCount:     len(annotation) / 4, // Rough token count approximation
	}
	
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Error encoding response: %v", err)
	}
}

// handleTags processes GET /api/tags requests
func handleTags(w http.ResponseWriter, r *http.Request) {
	log.Printf("[%s] %s %s", r.Method, r.URL.Path, r.RemoteAddr)
	
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	response := TagsResponse{
		Models: []ModelInfo{
			{
				Name:       "llava:latest",
				ModifiedAt: time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
				Size:       4700000000, // ~4.7GB
			},
		},
	}
	
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Error encoding response: %v", err)
	}
}

func main() {
	http.HandleFunc("/api/chat", handleChat)
	http.HandleFunc("/api/tags", handleTags)
	
	addr := ":11434"
	log.Printf("Mock Ollama API server starting on %s", addr)
	log.Printf("Endpoints:")
	log.Printf("  POST /api/chat - Chat completion with vision support")
	log.Printf("  GET  /api/tags - List available models")
	
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
