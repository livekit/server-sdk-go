// +build mage

package main

import (
	"fmt"
	"os"
	"os/exec"
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
