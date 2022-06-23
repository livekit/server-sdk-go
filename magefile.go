//go:build mage
// +build mage

package main

import (
	"fmt"
	"github.com/magefile/mage/sh"
	"os"
	"os/exec"
	"strings"
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
	testApiKey = "APIhdZda4TDAGxk: 4BA2qCorbmGnVZ9iMri7Sp0EEA7v2S4Oi8eyHuPxtxJ"
)

func Test() error {
	fmt.Println("starting livekit-server...")

	if err := sh.RunV(`docker`, `run`, `-d`, `--rm`, `-p7880:7880`, `-e`,
		fmt.Sprintf(`LIVEKIT_KEYS=%s`, testApiKey), `--name`, `livekit-server`, `livekit/livekit-server`); err != nil {
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
