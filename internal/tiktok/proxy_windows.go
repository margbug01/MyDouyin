package tiktok

import "golang.org/x/sys/windows/registry"

// readSystemProxies reads the proxy of Internet Options, which Clash and the
// like set when they take over the system's.
func readSystemProxies() systemProxies {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return systemProxies{}
	}
	defer k.Close()
	if on, _, err := k.GetIntegerValue("ProxyEnable"); err != nil || on == 0 {
		return systemProxies{}
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil {
		return systemProxies{}
	}
	return parseWinProxyServer(server)
}
