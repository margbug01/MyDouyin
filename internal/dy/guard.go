package dy

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Some endpoints, as comment replies, answer only a request that carries the
// ticket guard's headers (bd-ticket-guard-*), which Douyin's page signs with
// a key of the browser's: an HMAC-SHA256 of "ticket=…&path=…&timestamp=…"
// keyed by HKDF-SHA256 (no salt, no info) of the ECDH secret between the
// browser's private key and the key of Douyin's server certificate. The
// page keeps all of it in its localStorage (security-sdk/*), which ego lite
// exports beside the cookies; signed so, a request passes as the page's
// (checked against the page's own signature, 2026-11-16).

// guard is that material, as ego lite exports it.
type guard struct {
	Ticket     string `json:"ticket"`
	TsSign     string `json:"ts_sign"`
	PrivateKey string `json:"private_key"` // PEM, PKCS #8
	ServerCert string `json:"server_cert"` // PEM
}

var errNoGuard = errors.New("dy: no ticket guard material")

// guardFile is where ego lite exports the material, beside the cookies.
func guardFile() (string, error) {
	f, err := cookieFile()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(f), "guard.json"), nil
}

// guardHeaders signs a request to path, at now, as the page would.
func guardHeaders(path string, now time.Time) (http.Header, error) {
	file, err := guardFile()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNoGuard
	}
	if err != nil {
		return nil, err
	}
	var g guard
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, err
	}
	if g.Ticket == "" || g.TsSign == "" {
		return nil, errNoGuard
	}
	return g.sign(path, now)
}

func (g guard) sign(path string, now time.Time) (http.Header, error) {
	priv, err := pemKey(g.PrivateKey)
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode([]byte(g.ServerCert))
	if b == nil {
		return nil, errors.New("dy: ticket guard: bad server certificate")
	}
	cert, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return nil, err
	}
	serverKey, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("dy: ticket guard: server key not EC")
	}
	server, err := serverKey.ECDH()
	if err != nil {
		return nil, err
	}
	secret, err := priv.ECDH(server)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Key(sha256.New, secret, nil, "", 32)
	if err != nil {
		return nil, err
	}
	ts := now.Unix()
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "ticket=%s&path=%s&timestamp=%d", g.Ticket, path, ts)
	data, _ := json.Marshal(map[string]any{"ts_sign": g.TsSign, "req_content": "ticket,path,timestamp",
		"req_sign": base64.StdEncoding.EncodeToString(mac.Sum(nil)), "timestamp": ts})
	h := http.Header{}
	h.Set("bd-ticket-guard-client-data", base64.StdEncoding.EncodeToString(data))
	h.Set("bd-ticket-guard-ree-public-key", base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()))
	h.Set("bd-ticket-guard-version", "2")
	h.Set("bd-ticket-guard-web-version", "2")
	h.Set("bd-ticket-guard-web-sign-type", "1") // HMAC
	return h, nil
}

// pemKey reads an EC private key in PKCS #8.
func pemKey(s string) (*ecdh.PrivateKey, error) {
	b, _ := pem.Decode([]byte(s))
	if b == nil {
		return nil, errors.New("dy: ticket guard: bad private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("dy: ticket guard: private key not EC")
	}
	return ek.ECDH()
}
