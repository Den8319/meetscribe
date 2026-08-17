package llm

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Den8319/meetscribe/internal/models"
)

// GigaChatClient — реальная реализация llm.Client поверх GigaChat API (Sber).
type GigaChatClient struct {
	host      string
	oauthHost string
	authKey   string
	rqUID     string

	client *http.Client

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

// NewGigaChatClient создаёт клиент GigaChat.
func NewGigaChatClient(host, oauthHost, authKey, rqUID, caCertPath string) *GigaChatClient {
	return &GigaChatClient{
		host:      strings.TrimRight(host, "/"),
		oauthHost: strings.TrimRight(oauthHost, "/"),
		authKey:   authKey,
		rqUID:     rqUID,
		client:    newHTTPClient(caCertPath),
	}
}

// newHTTPClient строит http.Client. 
func newHTTPClient(caCertPath string) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if caCertPath != "" {
		pem, err := os.ReadFile(caCertPath)
		if err == nil {
			pool, _ := x509.SystemCertPool()
			if pool == nil {
				pool = x509.NewCertPool()
			}
			pool.AppendCertsFromPEM(pem)
			transport.TLSClientConfig = &tls.Config{RootCAs: pool}
		}
	}
	return &http.Client{Timeout: 60 * time.Second, Transport: transport}
}

// Summarize генерирует выжимку встречи по транскрипции.
func (c *GigaChatClient) Summarize(ctx context.Context, transcript string) (string, error) {
	prompt := "Ты - умный ИИ-ассистент, в задачу которого входит суммаризация и реферирование длинных текстов.\n## Основные правила:\n- Входные данные - текст. На выходе ты должен предоставить краткий текст, в котором содержатся основные идеи исходного текста.\n- Избегай длинных и сложных предложений. \n- Объём сокращённого текста должен составлять не более 1/5 от исходного текста.\n- Сохраняй основной смысл документа. \n- Ни один фрагмент сокращённого текста не должен искажать смысл исходного текста.\n- Не включай информацию, не являющуюся важной для текста: избегай выводов, оценок, примеров и цитат.\nПредставь ответ в виде структурированного текста с абзацами. Каждый абзац содержит одну основную мысль. Используй заголовки в формате Markdown (## Заголовок) перед каждым логически завершённым блоком. Текст должен быть удобочитаемым и чётко разделённым.\n\n" +
		"Выдели участников, тему, сроки и основные решения:\n\n" + transcript

	return c.chat(ctx, []message{{Role: "user", Content: prompt}})
}

// Chat отвечает на вопрос пользователя по контексту встречи.
func (c *GigaChatClient) Chat(ctx context.Context, contextText string, history []models.ChatMessage, question string) (string, error) {
	messages := make([]message, 0, len(history)+2)

	// Контекст встречи как системное сообщение.
	messages = append(messages, message{
		Role:    "system",
		Content: "Ты - умный ИИ-ассистент, в задачу которого входит суммаризация и реферирование длинных текстов.\nЗадача давать краткие ответы на вопросы пользователя.\n\n" + contextText,
	})

	// Предыдущая история диалога.
	for _, m := range history {
		messages = append(messages, message{Role: string(m.Role), Content: m.Message})
	}

	// Вопрос пользователя.
	messages = append(messages, message{Role: "user", Content: question})

	return c.chat(ctx, messages)
}


// message — элемент массива messages для /v1/chat/completions.
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatRequest — тело запроса chat completions.
type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
}

// chatResponse — ответ chat completions.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error"`
}

// chat отправляет сообщения в /v1/chat/completions и возвращает текст ответа.
func (c *GigaChatClient) chat(ctx context.Context, messages []message) (string, error) {
	token, err := c.token(ctx)
	if err != nil {
		return "", fmt.Errorf("gigachat auth: %w", err)
	}

	body, err := json.Marshal(chatRequest{Model: "GigaChat", Messages: messages})
	if err != nil {
		return "", fmt.Errorf("gigachat marshal request: %w", err)
	}

	u, err := url.JoinPath(c.host, "/api/v1/chat/completions")
	if err != nil {
		return "", fmt.Errorf("gigachat build url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("gigachat create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("gigachat request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("gigachat status %d: %s", resp.StatusCode, truncate(string(raw), 500))
	}

	var result chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("gigachat decode response: %w", err)
	}
	if result.Error != nil {
		return "", fmt.Errorf("gigachat api error %d: %s", result.Error.Code, result.Error.Message)
	}
	if len(result.Choices) == 0 || result.Choices[0].Message.Content == "" {
		return "", errors.New("gigachat: empty response")
	}

	return result.Choices[0].Message.Content, nil
}

// token возвращает действующий токен, при необходимости получая новый.
func (c *GigaChatClient) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Запас 30 секунд: перевыпускаем заранее, чтобы не попасть в истёкший токен.
	if c.accessToken != "" && time.Until(c.expiresAt) > 30*time.Second {
		return c.accessToken, nil
	}

	form := url.Values{"scope": {"GIGACHAT_API_PERS"}}
	u, err := url.JoinPath(c.oauthHost, "/api/v2/oauth")
	if err != nil {
		return "", fmt.Errorf("build oauth url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("create oauth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("RqUID", c.rqUID)
	req.Header.Set("Authorization", "Basic "+c.authKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("oauth request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("oauth status %d: %s", resp.StatusCode, truncate(string(raw), 500))
	}

	var auth struct {
		AccessToken string `json:"access_token"`
		ExpiresAt   int64  `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&auth); err != nil {
		return "", fmt.Errorf("oauth decode: %w", err)
	}
	if auth.AccessToken == "" {
		return "", errors.New("oauth: empty access_token")
	}

	c.accessToken = auth.AccessToken
	c.expiresAt = time.Unix(auth.ExpiresAt, 0)
	return c.accessToken, nil
}

// truncate обрезает строку до max символов (для логов ошибок).
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
