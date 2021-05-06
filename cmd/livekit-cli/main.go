package main

import (
	"fmt"
	"os"

	lksdk "github.com/livekit/livekit-sdk-go"
	"github.com/urfave/cli/v2"
)

func main() {
	app := &cli.App{
		Name:  "livekit-cli",
		Usage: "CLI client to LiveKit",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name: "verbose",
			},
		},
		Version: lksdk.Version,
	}

	app.Commands = append(app.Commands, RoomCommands...)
	app.Commands = append(app.Commands, TokenCommands...)

	if err := app.Run(os.Args); err != nil {
		fmt.Println(err)
	}
}
