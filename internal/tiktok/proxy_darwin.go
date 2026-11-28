package tiktok

import (
	"context"
	"os/exec"
	"time"
)

// readSystemProxies asks scutil for the proxies of the network in use, as
// System Settings › Network › Proxies sets them.
func readSystemProxies() systemProxies {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return systemProxies{}
	}
	return parseScutilProxy(string(out))
}
