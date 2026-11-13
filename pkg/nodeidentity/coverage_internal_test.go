package nodeidentity

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
)

// TestParsePrivateKeyPEMTypes covers the individual key formats and their
// failure modes in isolation from the certificate.
func TestParsePrivateKeyPEMTypes(t *testing.T) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalECPrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8DER, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		block   *pem.Block
		wantErr bool
	}{
		{"EC SEC1", &pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER}, false},
		{"PKCS8", &pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8DER}, false},
		{"RSA PKCS1", &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)}, false},
		{"garbage DER in EC block", &pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte{1, 2, 3}}, true},
		{"garbage DER in PKCS8 block", &pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1, 2, 3}}, true},
		{"unsupported PEM type", &pem.Block{Type: "MYSTERY KEY", Bytes: ecDER}, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := parsePrivateKeyPEM(tt.block)
			if tt.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestParsePrivateKeyPEMEd25519(t *testing.T) {
	// Kubernetes accepts ed25519 client certificates; a PKCS#8-wrapped
	// ed25519 key must parse, not be rejected as an unsupported algorithm.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = pub
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := parsePrivateKeyPEM(&pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		t.Fatalf("ed25519 PKCS#8 key must be accepted: %v", err)
	}
}
