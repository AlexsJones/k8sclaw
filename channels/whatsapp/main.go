// Package main is the entry point for the WhatsApp channel pod.
// Uses whatsmeow for WhatsApp Web multi-device protocol.
// On first run, a QR code is printed to the pod logs — scan it with
// WhatsApp → Linked Devices → Link a Device.
// Credentials are stored in an SQLite database on a PVC so the link
// survives pod restarts.
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

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	_ "modernc.org/sqlite" // pure-Go SQLite driver — no CGO required

	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/kubeclaw/kubeclaw/internal/channel"
	"github.com/kubeclaw/kubeclaw/internal/eventbus"
)

// WhatsAppChannel implements the WhatsApp Web channel via whatsmeow.
type WhatsAppChannel struct {
	channel.BaseChannel
	client  *whatsmeow.Client
	healthy bool
	log     waLog.Logger
}

func main() {
	var instanceName string
	var eventBusURL string
	var dataDir string
	var listenAddr string

	flag.StringVar(&instanceName, "instance", os.Getenv("INSTANCE_NAME"), "ClawInstance name")
	flag.StringVar(&eventBusURL, "event-bus-url", os.Getenv("EVENT_BUS_URL"), "Event bus URL")
	flag.StringVar(&dataDir, "data-dir", envOrDefault("WHATSAPP_DATA_DIR", "/data"), "Directory for SQLite credential store")
	flag.StringVar(&listenAddr, "addr", ":3000", "Listen address for health endpoint")
	flag.Parse()

	log := zap.New(zap.UseDevMode(false)).WithName("channel-whatsapp")
	waLogger := waLog.Stdout("WhatsApp", "INFO", true)

	bus, err := eventbus.NewNATSEventBus(eventBusURL)
	if err != nil {
		log.Error(err, "failed to connect to event bus")
		os.Exit(1)
	}
	defer bus.Close()

	// Initialise whatsmeow SQLite store
	dbPath := fmt.Sprintf("file:%s/whatsapp.db?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)", dataDir)
	container, err := sqlstore.New(context.Background(), "sqlite", dbPath, waLogger)
	if err != nil {
		log.Error(err, "failed to open credential store", "path", dbPath)
		os.Exit(1)
	}

	// Get the first device or create a new one
	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		log.Error(err, "failed to get device from store")
		os.Exit(1)
	}

	client := whatsmeow.NewClient(deviceStore, waLogger)

	wc := &WhatsAppChannel{
		BaseChannel: channel.BaseChannel{
			ChannelType:  "whatsapp",
			InstanceName: instanceName,
			EventBus:     bus,
		},
		client: client,
		log:    waLogger,
	}

	// Register event handler for incoming messages
	client.AddEventHandler(wc.eventHandler)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Connect — if not linked yet, show QR code
	if client.Store.ID == nil {
		log.Info("No WhatsApp session found — scan the QR code below to link this device")
		qrChan, _ := client.GetQRChannel(ctx)
		if err := client.Connect(); err != nil {
			log.Error(err, "failed to connect for QR pairing")
			os.Exit(1)
		}
		for evt := range qrChan {
			switch evt.Event {
			case "code":
				fmt.Println("\n╔══════════════════════════════════════════╗")
				fmt.Println("║  Scan this QR code in WhatsApp:          ║")
				fmt.Println("║  Settings → Linked Devices → Link Device ║")
				fmt.Println("╚══════════════════════════════════════════╝")
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
				fmt.Println()
			case "success":
				log.Info("WhatsApp device linked successfully!")
			case "timeout":
				log.Error(nil, "QR code timed out — restart the pod to try again")
				os.Exit(1)
			}
		}
	} else {
		if err := client.Connect(); err != nil {
			log.Error(err, "failed to connect")
			os.Exit(1)
		}
		log.Info("WhatsApp connected with existing session")
	}

	wc.healthy = true
	_ = wc.PublishHealth(ctx, channel.HealthStatus{Connected: true})

	go wc.handleOutbound(ctx)

	log.Info("WhatsApp channel running", "instance", instanceName, "addr", listenAddr)

	// Health & readiness server
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if wc.healthy && client.IsConnected() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		client.Disconnect()
		_ = server.Shutdown(shutdownCtx)
	}()

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error(err, "health server failed")
	}
}

// eventHandler processes whatsmeow events.
func (wc *WhatsAppChannel) eventHandler(evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		wc.handleInboundMessage(v)
	case *events.Connected:
		wc.healthy = true
		_ = wc.PublishHealth(context.Background(), channel.HealthStatus{Connected: true})
	case *events.Disconnected:
		wc.healthy = false
		_ = wc.PublishHealth(context.Background(), channel.HealthStatus{Connected: false, Message: "disconnected"})
	case *events.LoggedOut:
		wc.healthy = false
		fmt.Fprintln(os.Stderr, "WhatsApp session logged out — restart the pod and scan a new QR code")
		_ = wc.PublishHealth(context.Background(), channel.HealthStatus{Connected: false, Message: "logged out"})
	}
}

// handleInboundMessage processes a received WhatsApp message.
func (wc *WhatsAppChannel) handleInboundMessage(evt *events.Message) {
	// Skip status broadcasts and own messages
	if evt.Info.Chat.Server == "broadcast" || evt.Info.IsFromMe {
		return
	}

	text := extractText(evt.Message)
	if text == "" {
		return // skip non-text messages for now
	}

	senderName := evt.Info.PushName
	senderID := evt.Info.Sender.User
	chatID := evt.Info.Chat.User

	// For group chats, include group JID as chatID
	if evt.Info.IsGroup {
		chatID = evt.Info.Chat.String()
	}

	msg := channel.InboundMessage{
		SenderID:   senderID,
		SenderName: senderName,
		ChatID:     chatID,
		Text:       text,
		Metadata: map[string]string{
			"messageId": evt.Info.ID,
			"timestamp": fmt.Sprintf("%d", evt.Info.Timestamp.Unix()),
			"isGroup":   fmt.Sprintf("%t", evt.Info.IsGroup),
		},
	}

	if err := wc.PublishInbound(context.Background(), msg); err != nil {
		fmt.Fprintf(os.Stderr, "failed to publish inbound: %v\n", err)
	}
}

// handleOutbound subscribes to outbound messages and sends via WhatsApp.
func (wc *WhatsAppChannel) handleOutbound(ctx context.Context) {
	events, err := wc.SubscribeOutbound(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to subscribe to outbound: %v\n", err)
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
			if msg.Channel != "whatsapp" {
				continue
			}
			if err := wc.sendMessage(ctx, msg); err != nil {
				fmt.Fprintf(os.Stderr, "failed to send whatsapp message: %v\n", err)
			}
		}
	}
}

// sendMessage sends a text message via WhatsApp.
func (wc *WhatsAppChannel) sendMessage(ctx context.Context, msg channel.OutboundMessage) error {
	jid := resolveJID(msg.ChatID)

	_, err := wc.client.SendMessage(ctx, jid, &waE2E.Message{
		Conversation: proto.String(msg.Text),
	})
	return err
}

// resolveJID converts a chat ID string to a WhatsApp JID.
// If the ID already contains an @, assume it's a full JID.
// Otherwise treat it as a phone number (user JID).
func resolveJID(chatID string) types.JID {
	if strings.Contains(chatID, "@") {
		jid, _ := types.ParseJID(chatID)
		return jid
	}
	return types.NewJID(chatID, types.DefaultUserServer)
}

// extractText pulls the text content from a WhatsApp message proto.
func extractText(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if msg.Conversation != nil {
		return *msg.Conversation
	}
	if msg.ExtendedTextMessage != nil && msg.ExtendedTextMessage.Text != nil {
		return *msg.ExtendedTextMessage.Text
	}
	// Image/video/document captions
	if msg.ImageMessage != nil && msg.ImageMessage.Caption != nil {
		return *msg.ImageMessage.Caption
	}
	if msg.VideoMessage != nil && msg.VideoMessage.Caption != nil {
		return *msg.VideoMessage.Caption
	}
	if msg.DocumentMessage != nil && msg.DocumentMessage.Caption != nil {
		return *msg.DocumentMessage.Caption
	}
	return ""
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
