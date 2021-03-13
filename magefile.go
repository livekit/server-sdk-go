// +build mage

package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/magefile/mage/target"
)

// regenerate protobuf
func Proto() error {
	updated, err := target.Path("/proto/model.pb.go",
		"../livekit-server/proto/model.proto",
		"../livekit-server/proto/room.proto",
		"../livekit-server/proto/rtc.proto",
	)
	if err != nil {
		return err
	}
	if !updated {
		return nil
	}

	fmt.Println("generating protobuf")
	target := "proto/"
	if err := os.MkdirAll(target, 0755); err != nil {
		return err
	}

	// generate model and room
	cmd := exec.Command("protoc",
		"--go_out", target,
		"--twirp_out", target,
		"--go_opt=paths=source_relative",
		"--twirp_opt=paths=source_relative",
		"-I=../livekit-server/proto",
		"../livekit-server/proto/room.proto",
	)
	connectStd(cmd)
	if err := cmd.Run(); err != nil {
		return err
	}

	// generate rtc
	cmd = exec.Command("protoc",
		"--go_out", target,
		"--go_opt=paths=source_relative",
		"-I=../livekit-server/proto",
		"../livekit-server/proto/rtc.proto",
		"../livekit-server/proto/model.proto",
	)
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
