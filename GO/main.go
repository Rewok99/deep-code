package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/go-deepseek/deepseek"
	"github.com/go-deepseek/deepseek/request"
)

const (
	codeTemplate = `
	%s
`
)

type PageData struct {
	Code         string
	Output       string
	Error        string
	Compiled     bool
	ShowSolution bool
	Solution     string
	CodeTemplate string
}

type ChatRequest struct {
	Message string `json:"message"`
}

type ChatResponse struct {
	Response string `json:"response"`
}

var (
	client   deepseek.Client
	messages []*request.Message
)

func init() {
	var err error
	client, err = deepseek.NewClient("sk-9a10a8a18b24499f9ab962bc5750907f")
	if err != nil {
		log.Fatalf("Failed to create DeepSeek client: %v", err)
	}
	messages = make([]*request.Message, 0)
}

func main() {
	http.HandleFunc("/", homeHandler)
	http.HandleFunc("/run", runHandler)
	http.HandleFunc("/chat", chatHandler)

	port := ":8080"
	fmt.Printf("Server running on http://localhost%s\n", port)
	log.Fatal(http.ListenAndServe(port, nil))
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tmpl, err := template.ParseFiles("templates/home.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Создаем начальный шаблон кода
	initialTemplate := `package main

import (
    "fmt"
)

func main() {
    // Ваш код здесь
    fmt.Println("Hello, World!")
}`

	err = tmpl.Execute(w, PageData{
		CodeTemplate: initialTemplate,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func runHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	code := r.FormValue("code")
	if code == "" {
		http.Error(w, "Code cannot be empty", http.StatusBadRequest)
		return
	}

	// Оборачиваем код пользователя в main функцию
	fullCode := fmt.Sprintf(codeTemplate, code)

	output, err := compileAndRun(fullCode)
	data := PageData{
		Code:     code,
		Compiled: err == nil,
	}

	if err != nil {
		data.Error = err.Error()
		// Показываем ошибку сразу
		data.ShowSolution = false

		// Запускаем асинхронный запрос к DeepSeek
		go func() {

			if err != nil { // Добавляем проверку
				solution, solutionErr := getSolutionFromDeepSeek(code, err.Error())
				if solutionErr == nil {
					log.Println("Received solution from DeepSeek:", solution)
				}
			}
		}()
	} else {
		data.Output = output
	}

	tmpl, err := template.ParseFiles("templates/home.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	err = tmpl.Execute(w, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func compileAndRun(code string) (string, error) {
	// Создаем временный каталог
	dir, err := os.MkdirTemp("", "goexec")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	// Создаем временный файл с кодом
	srcFile := filepath.Join(dir, "main.go")
	err = os.WriteFile(srcFile, []byte(code), 0644)
	if err != nil {
		return "", fmt.Errorf("failed to write source file: %v", err)
	}

	// Компилируем программу
	outputFile := filepath.Join(dir, "main")
	if runtime.GOOS == "windows" {
		outputFile += ".exe"
	}

	cmd := exec.Command("go", "build", "-o", outputFile, srcFile)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err != nil {
		return "", fmt.Errorf("compilation error: %s", stderr.String())
	}

	// Выполняем скомпилированную программу
	cmd = exec.Command(outputFile)
	var stdout, stderrRun bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderrRun

	// Запускаем с таймаутом для предотвращения бесконечных циклов
	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()

	select {
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		return "", fmt.Errorf("execution timed out after 5 seconds")
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("runtime error: %s", stderrRun.String())
		}
	}

	return stdout.String(), nil
}

func getSolutionFromDeepSeek(code, errorMsg string) (string, error) {
	// Формируем запрос к DeepSeek
	prompt := fmt.Sprintf(
		"У меня есть Go код:\n%s\n\nОшибка:\n%s\n\nКратко объясни причину ошибки (1-2 предложения) и покажи только исправленный код. Ответь на русском языке. Не добавляй лишних пояснений.",
		code, errorMsg,
	)

	messages = append(messages, &request.Message{
		Role:    "user",
		Content: prompt,
	})

	chatReq := &request.ChatCompletionsRequest{
		Model:    deepseek.DEEPSEEK_CHAT_MODEL,
		Stream:   false,
		Messages: messages,
	}

	// Выполняем запрос
	chatResp, err := client.CallChatCompletionsChat(context.Background(), chatReq)
	if err != nil {
		return "", fmt.Errorf("failed to get response from DeepSeek: %v", err)
	}

	response := chatResp.Choices[0].Message.Content

	// Добавляем ответ ассистента в историю
	messages = append(messages, &request.Message{
		Role:    "assistant",
		Content: response,
	})

	return response, nil
}

func chatHandler(w http.ResponseWriter, r *http.Request) {
	// Настройка CORS
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Добавляем сообщение пользователя в историю
	messages = append(messages, &request.Message{
		Role:    "user",
		Content: req.Message,
	})

	// Формируем запрос к DeepSeek
	chatReq := &request.ChatCompletionsRequest{
		Model:    deepseek.DEEPSEEK_CHAT_MODEL,
		Stream:   false,
		Messages: messages,
	}

	// Выполняем запрос
	chatResp, err := client.CallChatCompletionsChat(r.Context(), chatReq)
	if err != nil {
		http.Error(w, "Failed to get response from DeepSeek", http.StatusInternalServerError)
		log.Printf("DeepSeek error: %v", err)
		return
	}

	response := chatResp.Choices[0].Message.Content

	// Добавляем ответ ассистента в историю
	messages = append(messages, &request.Message{
		Role:    "assistant",
		Content: response,
	})

	// Формируем и отправляем ответ
	resp := ChatResponse{
		Response: response,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}
