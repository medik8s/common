package nodeidentity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// issuePair generates a self-contained cert/key pair for parse-level tests.
func issuePair(t *testing.T, cn string, notBefore, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func TestParseClientPEM(t *testing.T) {
	now := time.Now()
	validCert, validKey := issuePair(t, "valid", now.Add(-time.Hour), now.Add(time.Hour))

	otherCert, _ := issuePair(t, "other", now.Add(-time.Hour), now.Add(time.Hour))
	_, otherKey := issuePair(t, "other-key", now.Add(-time.Hour), now.Add(time.Hour))

	expiredCert, expiredKey := issuePair(t, "expired", now.Add(-2*time.Hour), now.Add(-time.Hour))
	futureCert, futureKey := issuePair(t, "future", now.Add(time.Hour), now.Add(2*time.Hour))

	tests := []struct {
		name            string
		certPEM, keyPEM []byte
		wantErr         error
	}{
		{"valid pair", validCert, validKey, nil},
		{"expired", expiredCert, expiredKey, ErrCertExpired},
		{"not yet valid", futureCert, futureKey, ErrCertNotYetValid},
		{"key mismatch", validCert, otherKey, ErrCertKeyMismatch},
		{"cert mismatch", otherCert, validKey, ErrCertKeyMismatch},
		{"no cert PEM", []byte("garbage"), validKey, ErrCertUnavailable},
		{"no key PEM", validCert, []byte("garbage"), ErrCertUnavailable},
		{"empty inputs", nil, nil, ErrCertUnavailable},
		{"truncated cert DER", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1, 2, 3}}), validKey, ErrCertUnavailable},
		{"unsupported key type", validCert, pem.EncodeToMemory(&pem.Block{Type: "MYSTERY KEY", Bytes: []byte{1, 2, 3}}), ErrCertUnavailable},
		{"cert PEM wrong type", pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: validCert[27:100]}), validKey, ErrCertUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pair, err := parseClientPEM(tt.certPEM, tt.keyPEM, now)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if pair == nil || pair.Leaf == nil || pair.Leaf.Subject.CommonName != "valid" {
					t.Fatalf("unexpected pair: %+v", pair)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error, got none")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error %v does not match sentinel %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadClientCertificate(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	certPEM, keyPEM := issuePair(t, "disk", now.Add(-time.Hour), now.Add(time.Hour))
	certFile := filepath.Join(dir, "c.crt")
	keyFile := filepath.Join(dir, "c.key")
	if err := os.WriteFile(certFile, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := loadClientCertificate(certFile, keyFile, now); err != nil {
		t.Fatalf("valid files: %v", err)
	}
	if _, err := loadClientCertificate(filepath.Join(dir, "missing.crt"), keyFile, now); !errors.Is(err, ErrCertUnavailable) {
		t.Fatalf("missing cert file: %v", err)
	}
	if _, err := loadClientCertificate(certFile, filepath.Join(dir, "missing.key"), now); !errors.Is(err, ErrCertUnavailable) {
		t.Fatalf("missing key file: %v", err)
	}
}
