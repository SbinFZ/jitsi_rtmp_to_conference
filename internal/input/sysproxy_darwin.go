//go:build darwin

package input

import (
	"context"
	"os/exec"
	"time"
)

// readSystemProxy parses `scutil --proxy` (the settings shown in System
// Settings → Network → Proxies, and used by Safari/Chrome).
func readSystemProxy() (systemProxyConfig, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return systemProxyConfig{}, err
	}
	return parseScutil(string(out)), nil
}
