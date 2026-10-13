//go:build !darwin && !windows

package input

// Linux/BSD have no single system proxy store; HTTP(S)_PROXY environment
// variables (handled by the caller) are the convention.
func readSystemProxy() (systemProxyConfig, error) { return systemProxyConfig{}, nil }
