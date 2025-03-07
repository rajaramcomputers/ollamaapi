package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/russross/blackfriday/v2"
)

// Session storage (in-memory)
var (
	sessions   = make(map[string][]Message)
	sessionMut sync.Mutex
)

// Message represents a chat message
type Message struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// PageData holds data for the HTML template
type PageData struct {
	History []Message
}

// OllamaChatRequest defines the request body for Ollama's chat API
type OllamaChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream,omitempty"`
}

// OllamaChatResponse defines the response from Ollama's chat API
type OllamaChatResponse struct {
	Message Message `json:"message"`
	Done    bool    `json:"done"`
}

func main() {
	http.HandleFunc("/", homeHandler)
	http.HandleFunc("/chat", chatHandler)
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	log.Println("Server running on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", recoveryMiddleware(http.DefaultServeMux)))
}

// Home page handler
func homeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	funcMap := template.FuncMap{
		"title": func(s string) string {
			return strings.Title(s)
		},
		"safeHTML": func(content string) template.HTML {
			return template.HTML(content)
		},
	}

	sessionID := getSessionID(w, r)
	sessionMut.Lock()
	history := sessions[sessionID]
	sessionMut.Unlock()

	formattedHistory := make([]Message, len(history))
	for i, msg := range history {
		formattedHistory[i] = msg
		formattedHistory[i].Content = cleanResponse(msg.Content)
	}

	tmpl := template.Must(template.New("index.html").Funcs(funcMap).ParseFiles("templates/index.html"))
	err := tmpl.Execute(w, PageData{History: formattedHistory})
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		log.Printf("Template error: %v", err)
	}
}

// Chat handler with history
func chatHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := getSessionID(w, r)
	userMessage := r.FormValue("prompt")

	sessionMut.Lock()
	sessions[sessionID] = append(sessions[sessionID], Message{
		Role:    "user",
		Content: userMessage,
	})
	history := sessions[sessionID]
	sessionMut.Unlock()

	reqBody := OllamaChatRequest{
		Model:    "deepseek-r1:1.5b",
		Messages: history,
		Stream:   true, // Enable streaming
	}
	reqJSON, _ := json.Marshal(reqBody)

	resp, err := http.Post("http://localhost:11434/api/chat", "application/json", bytes.NewBuffer(reqJSON))
	if err != nil {
		http.Error(w, "Error communicating with Ollama", http.StatusInternalServerError)
		log.Printf("Ollama API error: %v", err)
		return
	}
	defer resp.Body.Close()

	var assistantResponse strings.Builder

	decoder := json.NewDecoder(resp.Body)
	for {
		var ollamaResp OllamaChatResponse
		if err := decoder.Decode(&ollamaResp); err == io.EOF {
			break
		} else if err != nil {
			http.Error(w, "Failed to parse response", http.StatusInternalServerError)
			log.Printf("JSON decode error: %v", err)
			return
		}

		assistantResponse.WriteString(ollamaResp.Message.Content)

		if ollamaResp.Done {
			break
		}
	}

	cleanedResponse := cleanResponse(assistantResponse.String())

	log.Printf("Cleaned Assistant Response: %s", cleanedResponse)

	sessionMut.Lock()
	sessions[sessionID] = append(sessions[sessionID], Message{
		Role:    "assistant",
		Content: cleanedResponse,
	})
	sessionMut.Unlock()

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// Clean up the response content and unescape HTML entities
func cleanResponse(content string) string {
	content = strings.ReplaceAll(content, "<think>", "")
	return string(blackfriday.Run([]byte(content)))
}

// Recovery middleware to catch panics
func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				log.Printf("Recovered from panic: %v", err)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// Get or create session ID
func getSessionID(w http.ResponseWriter, r *http.Request) string {
	cookie, err := r.Cookie("session_id")
	if err != nil {
		sessionID := generateSessionID()
		http.SetCookie(w, &http.Cookie{
			Name:    "session_id",
			Value:   sessionID,
			Expires: time.Now().Add(24 * time.Hour),
			Path:    "/",
		})
		return sessionID
	}
	return cookie.Value
}

// Generate secure session ID
func generateSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return "sess-" + base64.URLEncoding.EncodeToString(b)
}
