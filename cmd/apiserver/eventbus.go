package main

import (
	"context"
	"strings"
	"time"

	"github.com/sympozium-ai/sympozium/internal/eventbus"
)

func connectOptionalEventBus(url string) (eventbus.EventBus, error) {
	if strings.TrimSpace(url) == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bus, err := eventbus.NewNATSEventBusWithContext(ctx, url)
	if err != nil {
		return nil, err
	}
	return bus, nil
}
