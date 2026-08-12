package telegram

import (
	"context"
	"log/slog"

	tele "gopkg.in/telebot.v3"
)

func (b *Bot) AuthMiddleware(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		tgUser := c.Sender()
		if tgUser == nil {
			return c.Send("Could not identify the user")
		}

		ctx := context.Background()
		user, err := b.svc.RegisterUser(ctx, tgUser.ID, tgUser.Username)
		if err != nil {
			slog.Error("auth failed", "error", err, "tg_id", tgUser.ID)
			return c.Send("Authorization error")
		}

		c.Set("user", user)
		return next(c)
	}
}
