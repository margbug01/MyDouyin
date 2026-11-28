package tiktok

import (
	"os"
	"testing"
)

// TestMain points the config directory, where TikTok/cookies.json is kept, at
// a temporary one: the tests save cookies and log out, which on the real one
// would overwrite the user's TikTok login and then delete it.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "tiktok-test-")
	if err != nil {
		panic(err)
	}
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "APPDATA"} {
		os.Setenv(k, dir)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
