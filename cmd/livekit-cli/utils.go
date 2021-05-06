package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v2"
)

var (
	roomFlag = &cli.StringFlag{
		Name:     "room",
		Usage:    "name of the room",
		Required: true,
	}
	hostFlag = &cli.StringFlag{
		Name:    "host",
		EnvVars: []string{"LIVEKIT_HOST"},
		Value:   "http://localhost:7880",
	}
	apiKeyFlag = &cli.StringFlag{
		Name:     "api-key",
		EnvVars:  []string{"LIVEKIT_API_KEY"},
		Required: true,
	}
	secretFlag = &cli.StringFlag{
		Name:     "api-secret",
		EnvVars:  []string{"LIVEKIT_API_SECRET"},
		Required: true,
	}
	identityFlag = &cli.StringFlag{
		Name:     "identity",
		Usage:    "identity of participant",
		Required: true,
	}
)

func PrintJSON(obj interface{}) {
	txt, _ := json.MarshalIndent(obj, "", "  ")
	fmt.Println(string(txt))
}

func ExpandUser(p string) string {
	if strings.HasPrefix(p, "~") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[1:])
	}

	return p
}
