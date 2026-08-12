package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Den8319/meetscribe/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testRqUID — фиксированный UUID4 для тестов (валидный формат).
const testRqUID = "00000000-0000-0000-0000-000000000001"

// newTestGigaChat создаёт GigaChatClient, указывающий на httptest-сервер,
// который имитирует OAuth-авторизацию и chat completions.
func newTestGigaChat(t *testing.T, handler http.HandlerFunc) (*GigaChatClient, string) {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := NewGigaChatClient(srv.URL, srv.URL, "test-auth-key", testRqUID, "")
	return client, srv.URL
}

// TestGigaChat_Summarize проверяет полный цикл: oauth → chat completions → ответ.
func TestGigaChat_Summarize(t *testing.T) {
	var tokenCalls atomic.Int32

	client, _ := newTestGigaChat(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2/oauth":
			tokenCalls.Add(1)
			// Проверяем корректные заголовки авторизации.
			assert.Equal(t, "Basic test-auth-key", r.Header.Get("Authorization"))
			assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
			assert.Equal(t, testRqUID, r.Header.Get("RqUID"))

			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "test-token",
				"expires_at":   time.Now().Add(30 * time.Minute).Unix(),
			})

		case r.URL.Path == "/api/v1/chat/completions":
			assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
			var req chatRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			assert.Equal(t, "GigaChat", req.Model)
			require.NotEmpty(t, req.Messages)

			_ = json.NewEncoder(w).Encode(chatResponse{
				Choices: []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				}{{Message: struct {
					Content string `json:"content"`
				}{Content: "Выжимка встречи"}}},
			})

		default:
			http.NotFound(w, r)
		}
	})

	ctx := context.Background()
	res, err := client.Summarize(ctx, "текст транскрипции встречи")
	require.NoError(t, err)
	assert.Equal(t, "Выжимка встречи", res)

	// Токен запрашивается один раз, кэшируется.
	assert.Equal(t, int32(1), tokenCalls.Load())

	// Второй вызов не должен запрашивать токен заново.
	_, err = client.Chat(ctx, "контекст", nil, "Вопрос")
	require.NoError(t, err)
	assert.Equal(t, int32(1), tokenCalls.Load(), "token must be cached")
}

// TestGigaChat_TokenRefresh проверяет, что истёкший токен перевыпускается.
func TestGigaChat_TokenRefresh(t *testing.T) {
	var tokenCalls atomic.Int32

	client, _ := newTestGigaChat(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2/oauth":
			tokenCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "token-" + strconv.Itoa(int(tokenCalls.Load())),
				"expires_at":   time.Now().Add(5 * time.Second).Unix(),
			})
		case r.URL.Path == "/api/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(chatResponse{
				Choices: []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				}{{Message: struct {
					Content string `json:"content"`
				}{Content: "ok"}}},
			})
		default:
			http.NotFound(w, r)
		}
	})

	ctx := context.Background()

	_, err := client.Summarize(ctx, "текст")
	require.NoError(t, err)
	assert.Equal(t, int32(1), tokenCalls.Load())

	// Устанавливаем токен «протухшим» вручную — следующий вызов перевыпустит.
	client.mu.Lock()
	client.expiresAt = time.Now().Add(-time.Minute)
	client.mu.Unlock()

	_, err = client.Chat(ctx, "контекст", nil, "Вопрос")
	require.NoError(t, err)
	assert.Equal(t, int32(2), tokenCalls.Load(), "expired token must be refreshed")
}

// TestGigaChat_ApiError проверяет обработку ошибки API (не-200 статус).
func TestGigaChat_ApiError(t *testing.T) {
	client, _ := newTestGigaChat(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2/oauth":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "t",
				"expires_at":   time.Now().Add(time.Hour).Unix(),
			})
		case r.URL.Path == "/api/v1/chat/completions":
			http.Error(w, `{"error":{"message":"context limit","code":422}}`, http.StatusUnprocessableEntity)
		default:
			http.NotFound(w, r)
		}
	})

	_, err := client.Summarize(context.Background(), "текст")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "422")
	assert.Contains(t, err.Error(), "context limit")
}

// TestGigaChat_OAuthError проверяет ошибку авторизации.
func TestGigaChat_OAuthError(t *testing.T) {
	client, _ := newTestGigaChat(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/oauth" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	})

	_, err := client.Summarize(context.Background(), "текст")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

// TestGigaChat_ChatBuildsHistory проверяет формирование messages с историей.
func TestGigaChat_ChatBuildsHistory(t *testing.T) {
	var gotMessages []message

	client, _ := newTestGigaChat(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2/oauth":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "t",
				"expires_at":   time.Now().Add(time.Hour).Unix(),
			})
		case r.URL.Path == "/api/v1/chat/completions":
			var req chatRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			gotMessages = req.Messages
			_ = json.NewEncoder(w).Encode(chatResponse{
				Choices: []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				}{{Message: struct {
					Content string `json:"content"`
				}{Content: "ответ"}}},
			})
		default:
			http.NotFound(w, r)
		}
	})

	ctx := context.Background()
	_, err := client.Chat(ctx, "контекст встречи",
		[]models.ChatMessage{
			{Role: "user", Message: "вопрос 1"},
			{Role: "assistant", Message: "ответ 1"},
		},
		"вопрос 2")
	require.NoError(t, err)

	require.Len(t, gotMessages, 4)
	assert.Equal(t, "system", gotMessages[0].Role)
	assert.Contains(t, gotMessages[0].Content, "контекст встречи")
	assert.Equal(t, "user", gotMessages[1].Role)
	assert.Equal(t, "вопрос 1", gotMessages[1].Content)
	assert.Equal(t, "assistant", gotMessages[2].Role)
	assert.Equal(t, "вопрос 2", gotMessages[3].Content)
}

// TestGigaChat_EmptyResponse проверяет ошибку при пустом ответе модели.
func TestGigaChat_EmptyResponse(t *testing.T) {
	client, _ := newTestGigaChat(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2/oauth":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "t",
				"expires_at":   time.Now().Add(time.Hour).Unix(),
			})
		case r.URL.Path == "/api/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(chatResponse{Choices: nil})
		default:
			http.NotFound(w, r)
		}
	})

	_, err := client.Summarize(context.Background(), "текст")
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "empty response"), err.Error())
}
