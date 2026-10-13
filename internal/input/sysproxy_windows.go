//go:build windows

package input

import "golang.org/x/sys/windows/registry"

// readSystemProxy reads the WinINet proxy (Settings → Network → Proxy), the
// setting used by Edge/Chrome. PAC scripts (AutoConfigURL) are not evaluated.
func readSystemProxy() (systemProxyConfig, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return systemProxyConfig{}, err
	}
	defer k.Close()
	enabled, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return systemProxyConfig{}, nil
	}
	server, _, _ := k.GetStringValue("ProxyServer")
	override, _, _ := k.GetStringValue("ProxyOverride")
	return parseWinProxy(server, override), nil
}
