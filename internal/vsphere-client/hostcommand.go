package vsphereclient

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// HostCommand is the definition used for holding commands to be run on the fleeting
// host runner.
type HostCommand struct {
	Exe              string
	Args             []string
	WorkingDirectory string
	EnvVars          map[string]string
}

func (h *HostCommand) Run(ctx context.Context, specializedEnv map[string]string) error {
	command := exec.CommandContext(ctx, h.Exe, h.Args...)

	extraEnv := []string{}
	extraEnv = append(extraEnv, os.Environ()...)

	if h.EnvVars != nil {
		for k, v := range h.EnvVars {
			extraEnv = append(extraEnv, fmt.Sprintf("%s=%s", k, v))
		}
	}

	if specializedEnv != nil {
		for k, v := range specializedEnv {
			extraEnv = append(extraEnv, fmt.Sprintf("%s=%s", k, v))
		}
	}

	command.Env = extraEnv
	if h.WorkingDirectory != "" {
		command.Dir = h.WorkingDirectory
	}

	err := command.Start()
	if err != nil {
		return err
	}

	err = command.Wait()
	return err
}
