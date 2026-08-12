package telegram

import (
	"sync"

	"github.com/google/uuid"
)

// ChatManager хранит активные чат-сессии пользователей (FSM в памяти).
type ChatManager struct {
	mu       sync.Mutex
	sessions map[int64]uuid.UUID
}

func NewChatManager() *ChatManager {
	return &ChatManager{sessions: make(map[int64]uuid.UUID)}
}

// StartChat открывает сессию чата по встрече.
func (cm *ChatManager) StartChat(tgID int64, meetingID uuid.UUID) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.sessions[tgID] = meetingID
}

// StopChat закрывает сессию чата.
func (cm *ChatManager) StopChat(tgID int64) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.sessions, tgID)
}

// GetMeetingID возвращает ID встречи активной сессии.
func (cm *ChatManager) GetMeetingID(tgID int64) (uuid.UUID, bool) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	id, ok := cm.sessions[tgID]
	return id, ok
}
