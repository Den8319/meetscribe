package telegram

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Den8319/meetscribe/internal/models"
	"github.com/Den8319/meetscribe/internal/service"

	"github.com/google/uuid"
	tele "gopkg.in/telebot.v3"
)

func (b *Bot) OnStart(c tele.Context) error {
	return c.Send("MeetScribe — meeting notes assistant.\n\n" +
		"Commands:\n" +
		"/list — list meetings\n" +
		"/status <id> — processing status\n" +
		"/get <id> — transcript\n" +
		"/find <keyword> — search\n" +
		"/chat <id> — ask about a meeting\n" +
		"/exit — leave the chat.\n" +
		"Or upload audio or text")
}
func (b *Bot) OnList(c tele.Context) error {
	user := c.Get("user").(models.User)
	meetings, err := b.svc.ListMeetings(context.Background(), user.ID)
	if err != nil {
		return c.Send("Error: " + err.Error())
	}
	if len(meetings) == 0 {
		return c.Send("No meetings found!")
	}

	sb := new(strings.Builder)
	for _, m := range meetings {
		fmt.Fprintf(sb, "• %s | %s | %s\n", m.MeetingID, m.CreatedAt.Format(time.RFC3339), m.Status)
		if m.Summary != "" {			
			summary := m.Summary			
			fmt.Fprintf(sb, "   %s\n", summary)
		}
	}

	return c.Send(sb.String())
}
func (b *Bot) OnStatus(c tele.Context) error {
	user := c.Get("user").(models.User)
	args := c.Args()
	if len(args) < 1 {
		return c.Send("Usage: /status <id>")
	}

	meetingID, err := uuid.Parse(args[0])
	if err != nil {
		return c.Send("Invalid ID")
	}

	task, err := b.svc.GetMeetingStatus(context.Background(), user.ID, meetingID)
	if err != nil {
		return c.Send("Meeting not found")
	}

	text := fmt.Sprintf("Status: %s\nUpdated: %s", task.Status, task.UpdatedAt.Format(time.RFC3339))
	if task.ErrorMessage != "" {
		text += "\nError: " + task.ErrorMessage
	}
	return c.Send(text)
}
func (b *Bot) OnGet(c tele.Context) error {
	user := c.Get("user").(models.User)
	args := c.Args()
	if len(args) < 1 {
		return c.Send("Usage: /get <id>")
	}
	meetingID, err := uuid.Parse(args[0])
	if err != nil {
		return c.Send("Invalid ID")
	}
	tr, err := b.svc.GetTranscript(context.Background(), user.ID, meetingID)
	if err != nil {
		return c.Send("Meeting not found or transcript not ready")
	}

	text := tr.Text
	for len(text) > 4000 {
		if err := c.Send(text[:4000]); err != nil {
			return err
		}
		text = text[4000:]
	}
	return c.Send(text)
}

func (b *Bot) OnFind(c tele.Context) error {
	user := c.Get("user").(models.User)
	args := c.Args()
	if len(args) < 1 {
		return c.Send("Usage: /find <keyword>")
	}

	results, err := b.svc.Search(context.Background(), user.ID, args[0])
	if err != nil {
		return c.Send("Search error")
	}
	if len(results) == 0 {
		return c.Send("Nothing found")
	}

	var sb strings.Builder
	for _, r := range results {
		sb.WriteString(fmt.Sprintf("• %s | %s | %s\n   %s\n",
			r.MeetingID, r.CreatedAt.Format(time.RFC3339), r.Status, r.Snippet))
	}
	return c.Send(sb.String())
}
func (b *Bot) OnVoice(c tele.Context) error {
	user := c.Get("user").(models.User)
	msg := c.Message()
	if msg.Voice == nil {
		return c.Send("Voice not recognized")
	}

	// Лимит Telegram: файлы до 20 МБ
	if msg.Voice.FileSize > 20*1024*1024 {
		return c.Send("File too large (max 20 MB)")
	}

	// Скачиваем файл (telebot сам делает getFile + download)
	file, err := b.tb.FileByID(msg.Voice.FileID)
	if err != nil {
		return c.Send("File not found: " + err.Error())
	}
	reader, err := b.tb.File(&file)
	if err != nil {
		return c.Send("Error downloading file: " + err.Error())
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return c.Send("Error reading file")
	}

	// Создаём встречу + задачу в БД
	meeting, err := b.svc.UploadAndStartProcessing(context.Background(), user.ID, "Voice", data, "audio/ogg", "")
	if err != nil {
		return c.Send("Failed to create meeting: " + err.Error())
	}

	// Передаём аудио воркеру напрямую (sweeper шлёт nil!)
	b.wp.Submit(service.Job{
		MeetingID: meeting.ID,
		Audio:     data,
		MimeType:  "audio/ogg",
	})

	return c.Send(fmt.Sprintf("Voice downloaded! Meeting %s processing.", meeting.ID))

}
func (b *Bot) OnAudio(c tele.Context) error {
	user := c.Get("user").(models.User)
	msg := c.Message()
	if msg.Audio == nil {
		return c.Send("Audio not recognized")
	}

	// Лимит Telegram: файлы до 20 МБ
	if msg.Audio.FileSize > 20*1024*1024 {
		return c.Send("File too large (max 20 MB)")
	}

	// MIME-тип приходит с сервера Telegram
	mimeType := msg.Audio.MIME
	if mimeType == "" {
		mimeType = "audio/mpeg" // дефолт
	}

	// Скачиваем файл
	file, err := b.tb.FileByID(msg.Audio.FileID)
	if err != nil {
		return c.Send("File not found: " + err.Error())
	}
	reader, err := b.tb.File(&file)
	if err != nil {
		return c.Send("Error downloading file: " + err.Error())
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return c.Send("Error reading file")
	}

	// Создаём встречу + задачу
	meeting, err := b.svc.UploadAndStartProcessing(
		context.Background(), user.ID, "Audio file", data, mimeType, "")
	if err != nil {
		return c.Send("Failed to create meeting: " + err.Error())
	}

	// Передаём аудио воркеру
	b.wp.Submit(service.Job{
		MeetingID: meeting.ID,
		Audio:     data,
		MimeType:  mimeType,
	})

	return c.Send(fmt.Sprintf("Audio received! Meeting %s is processing.", meeting.ID))
}

func (b *Bot) OnDocument(c tele.Context) error {
	user := c.Get("user").(models.User)
	msg := c.Message()
	if msg.Document == nil {
		return c.Send("Document not recognized")
	}

	doc := msg.Document

	// Проверяем, что это текстовый файл
	if !strings.HasSuffix(doc.FileName, ".txt") && !strings.HasSuffix(doc.FileName, ".md") {
		return c.Send("Only .txt and .md files are supported")
	}

	// Скачиваем файл
	file, err := b.tb.FileByID(doc.FileID)
	if err != nil {
		return c.Send("File not found: " + err.Error())
	}
	reader, err := b.tb.File(&file)
	if err != nil {
		return c.Send("Error downloading file: " + err.Error())
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return c.Send("Error reading file")
	}

	// Создаём встречу с текстовым вводом (audio=nil, text=содержимое)
	meeting, err := b.svc.UploadAndStartProcessing(
		context.Background(), user.ID, doc.FileName, nil, "", string(data))
	if err != nil {
		return c.Send("Failed to create meeting: " + err.Error())
	}

	// Воркеру — Job с Text (speech не вызывается)
	b.wp.Submit(service.Job{
		MeetingID: meeting.ID,
		Text:      string(data),
	})

	return c.Send(fmt.Sprintf("Text file received! Meeting %s is processing.", meeting.ID))
}

func (b *Bot) OnSummary(c tele.Context) error {
	user := c.Get("user").(models.User)
	args := c.Args()
	if len(args) < 1 {
		return c.Send("Usage: /summary <id>")
	}
	meetingID, err := uuid.Parse(args[0])
	if err != nil {
		return c.Send("Invalid ID")
	}
	summary, err := b.svc.GetSummary(context.Background(), user.ID, meetingID)
	if err != nil {
		return c.Send("Summary not found or meeting not processed")
	}
	return c.Send(fmt.Sprintf("Summary:\n%s", summary.Text))
}

func (b *Bot) OnChat(c tele.Context) error {
	user := c.Get("user").(models.User)
	args := c.Args()
	if len(args) < 1 {
		return c.Send("Usage: /chat <id>")
	}
	meetingID, err := uuid.Parse(args[0])
	if err != nil {
		return c.Send("Invalid ID")
	}
	b.chat.StartChat(user.ExternalID, meetingID)
	return c.Send("Chat mode activated. Ask questions about the meeting. /exit — to leave.")
}

func (b *Bot) OnExit(c tele.Context) error {
	user := c.Get("user").(models.User)
	b.chat.StopChat(user.ExternalID)
	return c.Send("Chat ended.")
}

func (b *Bot) OnText(c tele.Context) error {
	user := c.Get("user").(models.User)
	meetingID, ok := b.chat.GetMeetingID(user.ExternalID)
	if !ok {
		return c.Send("I don't understand. Use /chat <id> to enter chat mode.")
	}
	answer, err := b.svc.AskQuestion(context.Background(), user.ID, meetingID, c.Text())
	if err != nil {
		return c.Send("Error: " + err.Error())
	}
	return c.Send(answer)
}
