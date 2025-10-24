package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const (
	toPhoneNumber = "919876543210"
)

var (
	host, apiKey, apiSecret string
	phoneNumberID           string
	verifyToken             string
	metaApiKey              string
	callDirection           string
)

func init() {
	flag.StringVar(&host, "host", "", "livekit server host")
	flag.StringVar(&apiKey, "api-key", "", "livekit api key")
	flag.StringVar(&apiSecret, "api-secret", "", "livekit api secret")
	flag.StringVar(&verifyToken, "webhook-verify-token", "", "whatsapp webhook verify token")
	flag.StringVar(&metaApiKey, "meta-api-key", "", "meta api key")
	flag.StringVar(&callDirection, "call-direction", "inbound", "call direction")
}

func main() {
	logger.InitFromConfig(&logger.Config{Level: "debug"}, "whatsapp_connector")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	direction := CallDirection(callDirection)

	connectorClient := lksdk.NewConnectorClient(host, apiKey, apiSecret)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	mux := http.NewServeMux()
	webhookHandler := newWebhookHandler(connectorClient, direction, verifyToken, metaApiKey)
	webhookHandler.SetupWebhookRoutes(mux)

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", 8080),
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		logger.Infow("WhatsApp connector started successfully")

		time.AfterFunc(5*time.Second, func() {
			if direction == CallDirectionOutbound {
				res, err := connectorClient.DialWhatsAppCall(ctx, &livekit.DialWhatsAppCallRequest{
					WhatsappPhoneNumberId: phoneNumberID,
					WhatsappToPhoneNumber: toPhoneNumber,
					WhatsappApiKey:        metaApiKey,
					DestinationCountry:    "US",
					RoomName:              "whatsapp-connector-test",
					ParticipantIdentity:   "test",
					ParticipantName:       "test",
				})
				if err != nil {
					logger.Errorw("Failed to dial whatsapp call", err)
				} else {
					logger.Infow("Whatsapp call dialed", "response", res)
				}
			}
		})

		logger.Infow("Starting HTTP server", "port", 8080)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorw("HTTP server failed", err)
			cancel()
		}
	}()

	select {
	case sig := <-sigChan:
		logger.Infow("Received shutdown signal", "signal", sig)
	case <-ctx.Done():
		logger.Infow("Context cancelled", nil)
	}

	logger.Infow("Shutting down gracefully...", nil)
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Errorw("HTTP server shutdown failed", err)
	}
}
