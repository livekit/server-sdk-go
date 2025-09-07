package main

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const (
	greeter = "greeter"
)

var (
	host, apiKey, apiSecret string
)

func init() {
	host = "ws://localhost:7880"
	apiKey = "devkey"
	apiSecret = "secret"
}

func performGreeting(room *lksdk.Room) {
	roomClient := lksdk.NewRoomServiceClient(host, apiKey, apiSecret)

	logger.Infow("[Server API] Triggering greeter's arrival RPC")
	res, err := roomClient.PerformRpc(context.Background(), &livekit.PerformRpcRequest{
		Room:                room.Name(),
		DestinationIdentity: greeter,
		Method:              "arrival",
		Payload:             "Hello",
	})

	if err != nil {
		logger.Errorw("[Server API] RPC call failed: ", err)
		return
	}

	logger.Infow(fmt.Sprintf("[Server API] That's nice, the greeter said: %s", res.GetPayload()))
}

func registerReceiverMethods(greeterRoom *lksdk.Room) error {
	arrivalHandler := func(data lksdk.RpcInvocationData) (string, error) {
		resultChan := make(chan string)

		logger.Infow(fmt.Sprintf("[Greeter] Oh %s arrived and said %s", data.CallerIdentity, data.Payload))

		time.AfterFunc(2000*time.Millisecond, func() {
			resultChan <- "Welcome and have a wonderful day!"
		})

		return <-resultChan, nil
	}
	err := greeterRoom.RegisterRpcMethod("arrival", arrivalHandler)
	if err != nil {
		logger.Errorw("Failed to register arrival RPC method", err)
		return err
	}

	return nil
}

func main() {
	logger.InitFromConfig(&logger.Config{Level: "info"}, "rpc-demo")
	lksdk.SetLogger(logger.GetLogger())

	roomName := "rpc-server-api" + uuid.New().String()[:8]
	logger.Infow("connecting participants to room", "roomName", roomName)

	room, err := lksdk.ConnectToRoom(host, lksdk.ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            roomName,
		ParticipantIdentity: greeter,
	}, nil)
	if err != nil {
		logger.Errorw("failed to connect to room", err, "participant", greeter)
	}
	defer room.Disconnect()

	err = registerReceiverMethods(room)
	if err != nil {
		logger.Errorw("failed to register RPC methods", err)
		return
	}

	logger.Infow("participants connected to room, starting rpc demo", "roomName", roomName)

	logger.Infow("Running greeting example...")
	performGreeting(room)

	time.Sleep(60 * time.Second)
}
