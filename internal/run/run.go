// Package run starts other programs, with a timeout.
package run

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

const Timeout = 10 * time.Second

// Start starts a command without waiting for it, because rebooting
// can end this process mid-request. Tests swap it for a recorder.
var Start = func(argv ...string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func Command(argv ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	return string(output), err
}

// WithInput runs a command without output pipes, because wl-copy keeps
// running in the background and would hold them open.
func WithInput(input string, argv ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(input)
	return cmd.Run()
}
