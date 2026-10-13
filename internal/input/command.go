package input

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// runInputCommand runs the --input-cmd shell command and returns the last
// non-empty stdout line (the fresh input URL). stderr is passed through for
// diagnostics but the URL itself is never logged unredacted.
func runInputCommand(ctx context.Context, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", command)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("--input-cmd failed: %v: %s", err, Redact(strings.TrimSpace(errOut.String())))
	}
	lines := strings.Split(strings.ReplaceAll(out.String(), "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line, nil
		}
	}
	return "", fmt.Errorf("--input-cmd printed no URL")
}
