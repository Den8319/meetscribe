package llm

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"time"

	"github.com/Den8319/meetscribe/internal/models"
)

// MockClient — тестовая реализация llm.Client.
// Summarize строит выжимку по шаблону из транскрипции, Chat отвечает на вопрос.
type MockClient struct {
	delayMin time.Duration
	delayMax time.Duration
}

// NewMock создаёт mock-клиент (generic option pattern).
func NewMock(opts ...Option[MockClient]) *MockClient {
	m := &MockClient{
		delayMin: 2 * time.Second,
		delayMax: 5 * time.Second,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Summarize генерирует выжимку встречи по тексту транскрипции.
func (m *MockClient) Summarize(ctx context.Context, transcript string) (string, error) {
	summary := buildSummary(transcript)

	if err := m.wait(ctx); err != nil {
		return "", err
	}
	return summary, nil
}

// Chat отвечает на вопрос по контексту встречи через keyword-matching:
func (m *MockClient) Chat(ctx context.Context, contextText string, history []models.ChatMessage, question string) (string, error) {
	answer := answerByKeywords(contextText, question)

	if err := m.wait(ctx); err != nil {
		return "", err
	}
	return answer, nil
}

// wait имитирует задержку LLM (2-5 секунд по умолчанию).
func (m *MockClient) wait(ctx context.Context) error {
	delay := m.delayMin
	if m.delayMax > m.delayMin {
		// Детерминированная "случайность" без math/rand.
		h := sha256.Sum256([]byte(time.Now().Format("15:04:05.000")))
		spread := m.delayMax - m.delayMin
		offset := time.Duration(binary.BigEndian.Uint32(h[:4])%uint32(spread)) % spread
		delay = m.delayMin + offset
	}

	select {
	case <-time.After(delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// buildSummary собирает выжимку: тема, участники, сроки, задачи.
func buildSummary(transcript string) string {
	lower := strings.ToLower(transcript)

	participants := extractParticipants(transcript)
	deadlines := extractDeadlines(lower)

	var sb strings.Builder
	sb.WriteString("Краткая выжимка встречи:\n")
	if len(participants) > 0 {
		sb.WriteString("Участники: " + strings.Join(participants, ", ") + ".\n")
	} else {
		sb.WriteString("Участники: не указаны.\n")
	}
	sb.WriteString("Тема: " + extractTopic(lower) + ".\n")
	if len(deadlines) > 0 {
		sb.WriteString("Сроки: " + strings.Join(deadlines, "; ") + ".\n")
	} else {
		sb.WriteString("Сроки: не указаны.\n")
	}
	sb.WriteString("Основные решения: обсуждены в полном тексте транскрипции.")
	return sb.String()
}

// extractParticipants находит имена людей в тексте (слова с заглавной буквы, кроме начала предложений).
func extractParticipants(text string) []string {
	names := []string{"Иван", "Мария", "Пётр", "Алексей", "Дмитрий", "Сергей", "Анна", "Кандидат", "Клиент"}
	var found []string
	seen := make(map[string]bool)
	for _, n := range names {
		if strings.Contains(text, n) && !seen[n] {
			seen[n] = true
			found = append(found, n)
		}
	}
	return found
}

// extractDeadlines находит предложения с датами/сроками.
func extractDeadlines(lower string) []string {
	var deadlines []string
	sentences := strings.SplitSeq(lower, ".")
	for s := range sentences {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if strings.Contains(s, "пятниц") || strings.Contains(s, "срок") ||
			strings.Contains(s, "дедлайн") || strings.Contains(s, "недел") ||
			strings.Contains(s, "завтра") {
			deadlines = append(deadlines, s)
		}
	}
	return deadlines
}

// extractTopic выделяет первое предложение как тему.
func extractTopic(lower string) string {
	sentences := strings.SplitSeq(lower, ".")
	for s := range sentences {
		s = strings.TrimSpace(s)
		if s != "" {
			return s
		}
	}
	return "не определена"
}

// answerByKeywords формирует ответ на вопрос по ключевым словам.
func answerByKeywords(contextText, question string) string {
	lowerQ := strings.ToLower(question)
	lowerCtx := strings.ToLower(contextText)

	switch {
	case strings.Contains(lowerQ, "иван"):
		if strings.Contains(lowerCtx, "иван") {
			return "Иван — участник встречи. Из контекста: " + sentenceWith(contextText, "Иван")
		}
		return "В контексте встречи Иван не упоминается."
	case strings.Contains(lowerQ, "мария"):
		if strings.Contains(lowerCtx, "мария") {
			return "Мария — участник встречи. Из контекста: " + sentenceWith(contextText, "Мария")
		}
		return "В контексте встречи Мария не упоминается."
	case strings.Contains(lowerQ, "когда") || strings.Contains(lowerQ, "срок") || strings.Contains(lowerQ, "пятниц"):
		if d := extractDeadlines(lowerCtx); len(d) > 0 {
			return "По итогам встречи: " + strings.Join(d, "; ") + "."
		}
		return "В контексте встречи сроки не указаны."
	case strings.Contains(lowerQ, "задач") || strings.Contains(lowerQ, "нужно") || strings.Contains(lowerQ, "сделать"):
		if strings.Contains(lowerCtx, "нужно") || strings.Contains(lowerCtx, "добавить") || strings.Contains(lowerCtx, "закончила") {
			return "Из контекста встречи: задачи обсуждались — " + sentenceWith(contextText, "нужно")
		}
		return "В контексте встречи задачи не найдены."
	default:
		return "По этому вопросу в контексте встречи недостаточно информации. Уточните вопрос или задайте его иначе."
	}
}

// sentenceWith возвращает первое предложение, содержащее ключевое слово.
func sentenceWith(text, keyword string) string {
	sentences := strings.SplitSeq(text, ".")
	for s := range sentences {
		if strings.Contains(s, keyword) {
			return strings.TrimSpace(s) + "."
		}
	}
	return ""
}
