package main

import (
	"flag"
	"time"

	"github.com/google/uuid"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const (
	sender   = "sender"
	receiver = "receiver"
)

var (
	host, apiKey, apiSecret string
)

func init() {
	flag.StringVar(&host, "host", "", "livekit server host")
	flag.StringVar(&apiKey, "api-key", "", "livekit api key")
	flag.StringVar(&apiSecret, "api-secret", "", "livekit api secret")
}

func main() {
	logger.InitFromConfig(&logger.Config{Level: "info"}, "ds-demo")
	lksdk.SetLogger(logger.GetLogger())
	flag.Parse()

	roomName := "datastreams-demo-" + uuid.New().String()[:8]
	senderRoom, err := lksdk.ConnectToRoom(host, lksdk.ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            roomName,
		ParticipantIdentity: sender,
	}, nil)
	if err != nil {
		logger.Errorw("failed to connect to room", err, "participant", sender)
		return
	}

	receiverRoom, err := lksdk.ConnectToRoom(host, lksdk.ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            roomName,
		ParticipantIdentity: receiver,
	}, nil)
	if err != nil {
		logger.Errorw("failed to connect to room", err, "participant", receiver)
		return
	}

	defer func() {
		senderRoom.Disconnect()
		receiverRoom.Disconnect()
	}()

	receiverRoom.RegisterTextStreamHandler("text-receive-iter", func(reader *lksdk.TextStreamReader, participantIdentity string) {
		logger.Infow("received text stream", "participant", participantIdentity)
		// Q. If I iterate over the reader directly without a goroutine, I don't receive any dc messages
		go func() {
			for chunk := range reader.Read() {
				logger.Infow("received text chunk", "chunk", chunk)
			}
		}()
	})

	receiverRoom.RegisterTextStreamHandler("text-receive-all", func(reader *lksdk.TextStreamReader, participantIdentity string) {
		logger.Infow("received text stream", "participant", participantIdentity)
		go func() {
			all := reader.ReadAll()
			logger.Infow("received text", "text", all)
		}()
	})

	// receiverRoom.RegisterTextStreamHandler("text-stream-recv-iter", func(reader *lksdk.TextStreamReader, participantIdentity string) {
	// 	logger.Infow("received text stream", "participant", participantIdentity)
	// 	go func() {
	// 		for chunk := range reader.Read() {
	// 			logger.Infow("received text chunk", "chunk", chunk)
	// 		}
	// 	}()
	// })

	// receiverRoom.RegisterTextStreamHandler("text-stream-recv-all", func(reader *lksdk.TextStreamReader, participantIdentity string) {
	// 	logger.Infow("received text stream", "participant", participantIdentity)
	// 	go func() {
	// 		all := reader.ReadAll()
	// 		logger.Infow("received text", "text", all)
	// 	}()
	// })

	// receiverRoom.RegisterByteStreamHandler("file-receive", func(reader *lksdk.ByteStreamReader, participantIdentity string) {
	// 	logger.Infow("received file stream", "participant", participantIdentity)
	// 	go func() {
	// 		all := reader.ReadAll()
	// 		os.WriteFile("received.pdf", all, 0644)
	// 	}()
	// })

	// Q. If I don't add a delay, I don't receive stream header, I do get chunk and trailer though
	time.Sleep(150 * time.Millisecond)

	text := "Lorem ipsum dolor sit amet..."
	topic := "text-receive-iter"
	senderRoom.LocalParticipant.SendText(text, lksdk.StreamTextOptions{
		Topic: topic,
	})

	// topic = "text-receive-all"
	// text = "Hello World"
	// senderRoom.LocalParticipant.SendText(text, lksdk.StreamTextOptions{
	// 	Topic: topic,
	// })

	// topic = "text-stream-recv-iter"
	// writer := senderRoom.LocalParticipant.StreamText(&lksdk.StreamTextOptions{
	// 	Topic: &topic,
	// })
	// for _, chunk := range strings.Split(text, " ") {
	// 	writer.Write(chunk)
	// }
	// writer.Close()

	// topic = "text-stream-recv-all"
	// writer = senderRoom.LocalParticipant.StreamText(&lksdk.StreamTextOptions{
	// 	Topic: &topic,
	// })
	// for _, chunk := range strings.Split(text, " ") {
	// 	writer.Write(chunk)
	// }
	// writer.Close()

	// fileName := "test.pdf"
	// senderRoom.LocalParticipant.SendFile(fileName, lksdk.StreamBytesOptions{
	// 	Topic:    "file-receive",
	// 	FileName: &fileName,
	// })

	time.Sleep(30 * time.Second)
}
