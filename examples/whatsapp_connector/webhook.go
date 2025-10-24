package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils/guid"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

type WebhookPayload struct {
	Object string         `json:"object"`
	Entry  []WebhookEntry `json:"entry"`
}

type WebhookEntry struct {
	ID      string          `json:"id"`
	Changes []WebhookChange `json:"changes"`
}

type WebhookChange struct {
	Value WebhookValue `json:"value"`
	Field string       `json:"field"`
}

type WebhookValue struct {
	MessagingProduct string     `json:"messaging_product"`
	Metadata         Metadata   `json:"metadata"`
	Calls            []Call     `json:"calls,omitempty"`
	Contacts         []Contact  `json:"contacts,omitempty"`
	Errors           []APIError `json:"errors,omitempty"`
	Statuses         []Status   `json:"statuses,omitempty"`
}

type Status struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Status    string `json:"status"`
}

type Metadata struct {
	PhoneNumberID      string `json:"phone_number_id"`
	DisplayPhoneNumber string `json:"display_phone_number"`
}

type Call struct {
	ID                    string   `json:"id"`
	To                    string   `json:"to"`
	From                  string   `json:"from"`
	Event                 string   `json:"event"`
	Direction             string   `json:"direction,omitempty"`
	Timestamp             string   `json:"timestamp"`
	StartTime             string   `json:"start_time,omitempty"`
	EndTime               string   `json:"end_time,omitempty"`
	Duration              int      `json:"duration,omitempty"`
	BizOpaqueCallbackData string   `json:"biz_opaque_callback_data,omitempty"`
	Session               *Session `json:"session,omitempty"`
}

type Session struct {
	SDPType string `json:"sdp_type"`
	SDP     string `json:"sdp"`
}

type Contact struct {
	Profile Profile `json:"profile"`
	WaID    string  `json:"wa_id"`
}

type Profile struct {
	Name string `json:"name"`
}

type APIError struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Href      string `json:"href,omitempty"`
	ErrorData struct {
		Details string `json:"details"`
	} `json:"error_data,omitempty"`
}

type CallDirection string

const (
	CallDirectionInbound  CallDirection = "inbound"
	CallDirectionOutbound CallDirection = "outbound"
)

type WebhookHandler struct {
	connectorClient *lksdk.ConnectorClient
	callDirection   CallDirection
	verifyToken     string
	metaApiKey      string
}

func newWebhookHandler(connectorClient *lksdk.ConnectorClient, callDirection CallDirection, verifyToken string, metaApiKey string) *WebhookHandler {
	return &WebhookHandler{
		connectorClient: connectorClient,
		callDirection:   callDirection,
		verifyToken:     verifyToken,
		metaApiKey:      metaApiKey,
	}
}

func (w *WebhookHandler) handleWebhook(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		w.handleWebhookVerification(writer, request)
		return
	}

	if request.Method == http.MethodPost {
		w.handleWebhookPayload(writer, request)
		return
	}

	http.Error(writer, "Method not allowed", http.StatusMethodNotAllowed)
}

func (w *WebhookHandler) handleWebhookVerification(writer http.ResponseWriter, request *http.Request) {
	mode := request.URL.Query().Get("hub.mode")
	token := request.URL.Query().Get("hub.verify_token")
	challenge := request.URL.Query().Get("hub.challenge")

	logger.Infow("Webhook verification request",
		"mode", mode,
		"token", token,
		"challenge", challenge)

	if mode == "subscribe" && token == w.verifyToken {
		logger.Infow("Webhook verification successful")
		writer.WriteHeader(http.StatusOK)
		writer.Write([]byte(challenge))
		return
	}

	logger.Errorw("Webhook verification failed", nil)
	http.Error(writer, "Forbidden", http.StatusForbidden)
}

func (w *WebhookHandler) handleWebhookPayload(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		logger.Errorw("Failed to read webhook body", err)
		http.Error(writer, "Bad request", http.StatusBadRequest)
		return
	}

	logger.Debugw("Received webhook payload", "body", string(body))

	var payload WebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		logger.Errorw("Failed to parse webhook payload", err, "payload", string(body))
		// Still respond with OK to avoid webhook retries
		writer.WriteHeader(http.StatusOK)
		writer.Write([]byte("OK"))
		return
	}

	for _, entry := range payload.Entry {
		for _, change := range entry.Changes {
			if change.Field == "calls" {
				w.handleCallsWebhook(change.Value)
			}
		}
	}

	writer.WriteHeader(http.StatusOK)
	writer.Write([]byte("OK"))
}

func (w *WebhookHandler) handleCallsWebhook(value WebhookValue) {
	for _, call := range value.Calls {
		switch call.Event {
		case "connect":
			ctx := context.Background()
			switch w.callDirection {
			case CallDirectionInbound:
				participantIdentity := guid.New("WAID_")[:8]
				participantName := call.From
				logger.Infow("Accepting whatsapp call", "call_id", call.ID, "from", call.From, "session_sdp", call.Session.SDP)
				res, err := w.connectorClient.AcceptWhatsAppCall(ctx, &livekit.AcceptWhatsAppCallRequest{
					WhatsappPhoneNumberId: value.Metadata.PhoneNumberID,
					WhatsappApiKey:        w.metaApiKey,
					WhatsappCallId:        call.ID,
					Sdp: &livekit.SessionDescription{
						Type: "offer",
						Sdp:  call.Session.SDP,
					},
					RoomName:            "whatsapp-connector-test",
					ParticipantIdentity: participantIdentity,
					ParticipantName:     participantName,
					DestinationCountry:  "US",
				})
				if err != nil {
					logger.Errorw("Failed to accept whatsapp call", err)
				} else {
					logger.Infow("Whatsapp call accepted", "response", res)

					go func() {
						time.AfterFunc(12*time.Second, func() {
							w.connectorClient.DisconnectWhatsAppCall(ctx, &livekit.DisconnectWhatsAppCallRequest{
								WhatsappApiKey: w.metaApiKey,
								WhatsappCallId: call.ID,
							})
						})
					}()
				}
			case CallDirectionOutbound:
				res, err := w.connectorClient.ConnectWhatsAppCall(ctx, &livekit.ConnectWhatsAppCallRequest{
					WhatsappPhoneNumberId: value.Metadata.PhoneNumberID,
					WhatsappApiKey:        w.metaApiKey,
					WhatsappCallId:        call.ID,
					Sdp: &livekit.SessionDescription{
						Type: "answer",
						Sdp:  call.Session.SDP,
					},
				})
				if err != nil {
					logger.Errorw("Failed to connect whatsapp call", err)
				} else {
					logger.Infow("Whatsapp call connected", "response", res)
				}
			}
		case "terminate":
			// no-op
		default:
			logger.Warnw("Unknown call event", nil, "event", call.Event, "call_id", call.ID)
		}
	}
}

func (w *WebhookHandler) SetupWebhookRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/webhook", w.handleWebhook)
	mux.HandleFunc("/health", func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		writer.Write([]byte("OK"))
	})
}
