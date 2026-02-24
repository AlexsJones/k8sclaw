// Package main is the entry point for the Telegram channel pod.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/kubeclaw/kubeclaw/internal/channel"
	"github.com/kubeclaw/kubeclaw/internal/eventbus"
)

// TelegramChannel implements the Telegram Bot API channel.
type TelegramChannel struct {
	channel.BaseChannel
	BotToken string
	client   *http.Client
	healthy  bool
}

func main() {
	var instanceName string
	var eventBusURL string
	var botToken string

	flag.StringVar(&instanceName, "instance", os.Getenv("INSTANCE_NAME"), "ClawInstance name")
	flag.StringVar(&eventBusURL, "event-bus-url", os.Getenv("EVENT_BUS_URL"), "Event bus URL")
	flag.StringVar(&botToken, "bot-token", os.Getenv("TELEGRAM_BOT_TOKEN"), "Telegram Bot API token")
	flag.Parse()

	if botToken == "" {
		fmt.Fprintln(os.Stderr, "TELEGRAM_BOT_TOKEN is required")
		os.Exit(1)
	}

	log := zap.New(zap.UseDevMode(false)).WithName("channel-telegram")

	bus, err := eventbus.NewNATSEventBus(eventBusURL)
	if err != nil {
		log.Error(err, "failed to connect to event bus")
		os.Exit(1)
	}
	defer bus.Close()

	ch := &TelegramChannel{
		BaseChannel: channel.BaseChannel{
			ChannelType:  "telegram",
			InstanceName: instanceName,
			EventBus:     bus,
		},
		BotToken: botToken,
		client:   &http.Client{Timeout: 30 * time.Second},
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Health server
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			if ch.healthy {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		})
		_ = http.ListenAndServe(":8080", mux)
	}()

	go ch.handleOutbound(ctx)

	log.Info("Starting Telegram channel", "instance", instanceName)
	if err := ch.pollUpdates(ctx); err != nil {
		log.Error(err, "telegram polling failed")
	}
}

// pollUpdates uses Telegram's long-polling getUpdates API.
func (tc *TelegramChannel) pollUpdates(ctx context.Context) error {
	offset := 0
	tc.healthy = true
	_ = tc.PublishHealth(ctx, channel.HealthStatus{Connected: true})

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=30", tc.BotToken, offset)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}

		resp, err := tc.client.Do(req)
		if err != nil {
			tc.healthy = false
			_ = tc.PublishHealth(ctx, channel.HealthStatus{Connected: false, Message: err.Error()})
			time.Sleep(5 * time.Second)
			continue
		}

		var result struct {
			OK     bool `json:"ok"`
			Result []struct {
				UpdateID int `json:"update_id"`
				Message  *struct {
					MessageID int `json:"message_id"`
					From      struct {
						ID       int64  `json:"id"`
						Username string `json:"username"`
						Name     string `json:"first_name"`
					} `json:"from"`
					Chat struct {
						ID   int64  `json:"id"`
						Type string `json:"type"`
					} `json:"chat"`
					Text string `json:"text"`
				} `json:"message"`
			} `json:"result"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		tc.healthy = true

		for _, update := range result.Result {
			offset = update.UpdateID + 1
			if update.Message == nil || update.Message.Text == "" {
				continue
			}

			msg := channel.InboundMessage{
				SenderID:   fmt.Sprintf("%d", update.Message.From.ID),
				SenderName: update.Message.From.Name,
				ChatID:     fmt.Sprintf("%d", update.Message.Chat.ID),
				Text:       update.Message.Text,
				Metadata: map[string]string{
					"messageId": fmt.Sprintf("%d", update.Message.MessageID),
					"username":  update.Message.From.Username,
					"chatType":  update.Message.Chat.Type,
				},
			}

			if err := tc.PublishInbound(ctx, msg); err != nil {
				fmt.Fprintf(os.Stderr, "failed to publish inbound: %v\n", err)
			}
		}
	}
}

// handleOutbound subscribes to outbound messages and sends them via the Bot API.
func (tc *TelegramChannel) handleOutbound(ctx context.Context) {
	events, err := tc.SubscribeOutbound(ctx)
	if err != nil {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case event := <-events:
			var msg channel.OutboundMessage
			if err := json.Unmarshal(event.Data, &msg); err != nil {
				continue
			}
			if msg.Channel != "telegram" {
				continue
			}
			_ = tc.sendMessage(ctx, msg)
		}
	}
}

// sendMessage sends a message via the Telegram Bot API.
func (tc *TelegramChannel) sendMessage(ctx context.Context, msg channel.OutboundMessage) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", tc.BotToken)

	parseMode := "Markdown"
	if msg.Format == "html" {
		parseMode = "HTML"
	}

	payload := map[string]interface{}{
		"chat_id":    msg.ChatID,
		"text":       msg.Text,
		"parse_mode": parseMode,
	}
	if msg.ReplyTo != "" {
		payload["reply_to_message_id"] = msg.ReplyTo
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
		strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := tc.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}
