package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	lksdk "github.com/livekit/livekit-sdk-go"
	"github.com/urfave/cli/v2"
)

var (
	JoinCommands = []*cli.Command{
		{
			Name:   "join-room",
			Usage:  "joins a room as a client",
			Action: joinRoom,
			Flags: []cli.Flag{
				hostFlag,
				roomFlag,
				identityFlag,
				apiKeyFlag,
				secretFlag,
			},
		},
	}
)

func joinRoom(c *cli.Context) error {
	room, err := lksdk.ConnectToRoom(c.String("host"), lksdk.ConnectInfo{
		APIKey:              c.String("api-key"),
		APISecret:           c.String("api-secret"),
		RoomName:            c.String("room"),
		ParticipantIdentity: c.String("identity"),
	})
	if err != nil {
		return err
	}

	fmt.Println("connected to room", room.Name)

	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	<-done
	room.Disconnect()
	return nil
}
