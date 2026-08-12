package telegram

import (
	"time"

	"github.com/Den8319/meetscribe/internal/service"

	tele "gopkg.in/telebot.v3"
)

type Bot struct {
	tb   *tele.Bot
	svc  *service.MeetingService
	wp   *service.WorkerPool
	chat *ChatManager
}

func New(token string, svc *service.MeetingService, wp *service.WorkerPool) (*Bot, error) {

	teleBot, err := tele.NewBot(tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		return nil, err
	}

	b := &Bot{
		tb:   teleBot,
		svc:  svc,
		wp:   wp,
		chat: NewChatManager(),
	}

	b.tb.Use(b.AuthMiddleware)

	b.registerHandlers()

	return b, nil
}

func (b *Bot) Start() { b.tb.Start() }
func (b *Bot) Stop()  { b.tb.Stop() }

func (b *Bot) registerHandlers() {
	b.tb.Handle("/start", b.OnStart)
	b.tb.Handle("/list", b.OnList)
	b.tb.Handle("/status", b.OnStatus)
	b.tb.Handle("/get", b.OnGet)
	b.tb.Handle("/summary", b.OnSummary)
	b.tb.Handle("/find", b.OnFind)
	b.tb.Handle("/chat", b.OnChat)
	b.tb.Handle("/exit", b.OnExit)
	b.tb.Handle(tele.OnVoice, b.OnVoice)
	b.tb.Handle(tele.OnAudio, b.OnAudio)
	b.tb.Handle(tele.OnDocument, b.OnDocument)
	b.tb.Handle(tele.OnText, b.OnText)
}
