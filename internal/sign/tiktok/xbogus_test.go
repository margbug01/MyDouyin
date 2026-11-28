package tiktok

import (
	"strings"
	"testing"
)

func TestSignXBogusFormatAndDeterminism(t *testing.T) {
	query := "aid=1988&app_name=tiktok_web&count=15&device_platform=web_pc"
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

	s1 := SignXBogus(query, ua, 1730000000)
	s2 := SignXBogus(query, ua, 1730000000)
	if len(s1) != 28 {
		t.Fatalf("len(SignXBogus) = %d, want 28 (%q)", len(s1), s1)
	}
	if s1 != s2 {
		t.Fatalf("SignXBogus not deterministic: %q != %q", s1, s2)
	}
	for _, r := range s1 {
		if !strings.ContainsRune(xbogusAlphabet, r) {
			t.Fatalf("unexpected character %q in X-Bogus %q", r, s1)
		}
	}
	s3 := SignXBogus(query+"&cursor=20", ua, 1730000000)
	if s3 == s1 {
		t.Fatalf("different query produced identical X-Bogus %q", s3)
	}
}
