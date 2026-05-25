package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// Notifier sends operational events to external channels (Telegram, etc.).
// Implementations must be safe for concurrent use.
type Notifier interface {
	Send(ctx context.Context, message string)
}

// NopNotifier discards all messages.
type NopNotifier struct{}

func (NopNotifier) Send(context.Context, string) {}

// TelegramNotifier posts messages to a Telegram chat via the Bot API.
type TelegramNotifier struct {
	botToken string
	chatID   string
	client   *http.Client
}

// NewTelegramNotifier loads BILLING_TELEGRAM_BOT_TOKEN and
// BILLING_TELEGRAM_CHAT_ID from the environment and returns a live
// notifier. Returns NopNotifier when either is missing.
func NewTelegramNotifierFromEnv() Notifier {
	token := strings.TrimSpace(os.Getenv("BILLING_TELEGRAM_BOT_TOKEN"))
	chat := strings.TrimSpace(os.Getenv("BILLING_TELEGRAM_CHAT_ID"))
	if token == "" || chat == "" {
		return NopNotifier{}
	}
	return &TelegramNotifier{
		botToken: token,
		chatID:   chat,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (t *TelegramNotifier) Send(ctx context.Context, message string) {
	if t == nil || t.botToken == "" {
		return
	}
	go func() {
		sendCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.botToken)
		payload, _ := json.Marshal(map[string]string{
			"chat_id":    t.chatID,
			"text":       message,
			"parse_mode": "Markdown",
		})
		req, err := http.NewRequestWithContext(sendCtx, http.MethodPost, url, strings.NewReader(string(payload)))
		if err != nil {
			log.WithError(err).Debug("billing: telegram notify build request failed")
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := t.client.Do(req)
		if err != nil {
			log.WithError(err).Debug("billing: telegram notify failed")
			return
		}
		_ = resp.Body.Close()
	}()
}
