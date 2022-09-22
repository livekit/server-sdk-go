//go:build mage
// +build mage

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/magefile/mage/sh"
)

var Default = Build

func Build() error {
	fmt.Println("building...")
	cmd := exec.Command("go", "build", ".")
	connectStd(cmd)
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func connectStd(cmd *exec.Cmd) {
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
}

const (
	testApiKey = "devkey: secret"
)

func Test() error {
	fmt.Println("starting livekit-server...")

	if err := sh.RunV(`docker`, `run`,
		`-e`, `LIVEKIT_KEYS=`+testApiKey,
		`-d`, `--rm`, `-p7880:7880`, `-p7881:7881`, `-p7882:7882/udp`,
		`--name`, `livekit-server`,
		`livekit/livekit-server`, `--dev`); err != nil {
		return err
	}
	defer func() {
		fmt.Println("stop livekit-server...")
		run(nil, "docker stop livekit-server")
	}()

	fmt.Println("testing...")
	testflags := os.Getenv("TestFlags")
	return run(map[string]string{"LIVEKIT_KEYS": testApiKey}, `go test -race `+testflags)
}

func run(env map[string]string, commands ...string) error {
	for _, command := range commands {
		args := strings.Split(command, " ")
		_, err := sh.Exec(env, os.Stdout, os.Stderr, args[0], args[1:]...)
		if err != nil {
			return err
		}
	}
	return nil
}
