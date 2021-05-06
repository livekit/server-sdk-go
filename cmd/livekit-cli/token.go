package main

import (
	"fmt"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/urfave/cli/v2"
)

var (
	TokenCommands = []*cli.Command{
		{
			Name:   "create-token",
			Usage:  "creates an access token",
			Action: createToken,
			Flags: []cli.Flag{
				apiKeyFlag,
				secretFlag,
				&cli.BoolFlag{
					Name:  "create",
					Usage: "enable token to be used to create rooms",
				},
				&cli.BoolFlag{
					Name:  "list",
					Usage: "enable token to be used to list rooms",
				},
				&cli.BoolFlag{
					Name:  "join",
					Usage: "enable token to be used to join a room (requires --room and --identity)",
				},
				&cli.BoolFlag{
					Name:  "admin",
					Usage: "enable token to be used to manage a room (requires --room)",
				},
				&cli.StringFlag{
					Name:    "identity",
					Aliases: []string{"i"},
					Usage:   "unique identity of the participant, used with --join",
				},
				&cli.StringFlag{
					Name:    "room",
					Aliases: []string{"r"},
					Usage:   "name of the room to join, empty to allow joining all rooms",
				},
				&cli.StringFlag{
					Name:  "metadata",
					Usage: "JSON metadata to encode in the token, will be passed to participant",
				},
				&cli.StringFlag{
					Name:  "valid-for",
					Usage: "amount of time that the token is valid for. i.e. \"5m\", \"1h10m\"",
					Value: "5m",
				},
			},
		},
	}
)

func createToken(c *cli.Context) error {
	if !c.IsSet("api-key") || !c.IsSet("api-secret") {
		return fmt.Errorf("api-key and api-secret are required")
	}
	p := c.String("identity") // required only for join
	room := c.String("room")
	metadata := c.String("metadata")
	validFor := c.String("valid-for")

	grant := &auth.VideoGrant{}
	if c.Bool("create") {
		grant.RoomCreate = true
	}
	if c.Bool("join") {
		grant.RoomJoin = true
		grant.Room = room
		if p == "" {
			return fmt.Errorf("participant name is required")
		}
	}
	if c.Bool("admin") {
		grant.RoomAdmin = true
		grant.Room = room
	}
	if c.Bool("list") {
		grant.RoomList = true
	}

	if !grant.RoomJoin && !grant.RoomCreate && !grant.RoomAdmin && !grant.RoomList {
		return fmt.Errorf("at least one of --list, --join, --create, or --admin is required")
	}

	at := accessToken(c, grant, p)

	if metadata != "" {
		at.SetMetadata(metadata)
	}
	if validFor != "" {
		if dur, err := time.ParseDuration(validFor); err == nil {
			at.SetValidFor(dur)
		}
	}

	token, err := at.ToJWT()
	if err != nil {
		return err
	}

	fmt.Println("token grants")
	PrintJSON(grant)
	fmt.Println()
	fmt.Println("access token: ", token)
	return nil
}

func accessToken(c *cli.Context, grant *auth.VideoGrant, identity string) *auth.AccessToken {
	apiKey := c.String("api-key")
	apiSecret := c.String("api-secret")
	if apiKey == "" && apiSecret == "" {
		// not provided, don't sign request
		return nil
	}
	at := auth.NewAccessToken(apiKey, apiSecret).
		AddGrant(grant).
		SetIdentity(identity)
	return at
}
