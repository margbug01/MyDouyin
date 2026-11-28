//go:build !darwin && !windows

package tiktok

// readSystemProxies finds none: elsewhere the environment's is the system's.
func readSystemProxies() systemProxies { return systemProxies{} }
