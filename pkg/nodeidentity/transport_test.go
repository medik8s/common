package nodeidentity_test

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/medik8s/common/pkg/nodeidentity"
)

// copyFile overwrites dst with the contents of src.
func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// transport tests exercise the real transport (NewTransport path) against a
// TLS test server that requires and verifies client certificates.

func TestTransportHappyPath(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	api := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, api.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)

	node, err := c.GetOwnNode(context.Background())
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if node.Metadata.Name != "worker-1" || node.Metadata.UID != "uid-1" {
		t.Fatalf("unexpected node: %+v", node.Metadata)
	}
}

func TestTransportWrongCA(t *testing.T) {
	pki := newTestPKI(t)
	other := newTestPKI(t) // different CA signs the server cert
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	api := newFakeNodeAPI("worker-1", "uid-1")
	srv := other.newTestServer(t, api.handler())
	// client trusts pki's CA, server presents a cert signed by other's CA
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)

	if _, err := c.GetOwnNode(context.Background()); !errors.Is(err, nodeidentity.ErrAPIServer) {
		t.Fatalf("expected ErrAPIServer for untrusted server, got %v", err)
	}
}

func TestTransportTLSVersionFloor(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	api := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, api.handler())
	// Cap the server at TLS 1.1: the client requires TLS 1.2 and must refuse.
	srv.TLS.MaxVersion = tls.VersionTLS11
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)

	if _, err := c.GetOwnNode(context.Background()); !errors.Is(err, nodeidentity.ErrAPIServer) {
		t.Fatalf("expected ErrAPIServer for TLS 1.1-only server, got %v", err)
	}
}

func TestTransportIgnoresHTTPProxy(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	api := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, api.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)

	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	t.Setenv("https_proxy", "http://127.0.0.1:1")

	if _, err := c.GetOwnNode(context.Background()); err != nil {
		t.Fatalf("proxy env must be ignored (Proxy: nil): %v", err)
	}
}

func TestTransportHTTP1OnlyAndNoConnectionReuse(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	api := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, api.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)

	const requests = 5
	for i := 0; i < requests; i++ {
		if _, err := c.GetOwnNode(context.Background()); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}

	api.mu.Lock()
	proto, handled := api.proto, len(api.peerCNs)
	api.mu.Unlock()
	if proto != "HTTP/1.1" {
		t.Fatalf("client must negotiate HTTP/1.1 only, got %q", proto)
	}
	if handled != requests {
		t.Fatalf("expected %d requests handled, got %d", requests, handled)
	}
}

func TestTransportPerHandshakeCertificateFreshness(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "first-cn", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	api := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, api.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)

	if _, err := c.GetOwnNode(context.Background()); err != nil {
		t.Fatalf("first request: %v", err)
	}

	// Replace the pair on disk (a rotation that completed before the hold).
	newCert, newKey := pki.writeClientPair(t, "second-cn", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err := copyFile(certFile, newCert); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(keyFile, newKey); err != nil {
		t.Fatal(err)
	}

	if _, err := c.GetOwnNode(context.Background()); err != nil {
		t.Fatalf("second request after rotation: %v", err)
	}

	api.mu.Lock()
	cns := append([]string(nil), api.peerCNs...)
	api.mu.Unlock()
	if len(cns) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(cns))
	}
	if cns[0] != "first-cn" || cns[1] != "second-cn" {
		t.Fatalf("per-handshake freshness violated: peer CNs = %v", cns)
	}
}

func TestTransportExpiredCertificateFailsFast(t *testing.T) {
	pki := newTestPKI(t)
	// Expired pair on disk: the client must fail with the typed error before
	// any network I/O (the fake server would otherwise reject it too, but the
	// typed error must not depend on server behavior).
	certFile, keyFile := pki.writeClientPair(t, "expired", time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	api := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, api.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)

	if _, err := c.GetOwnNode(context.Background()); !errors.Is(err, nodeidentity.ErrCertExpired) {
		t.Fatalf("expected ErrCertExpired, got %v", err)
	}

	api.mu.Lock()
	gets := api.gets
	api.mu.Unlock()
	if gets != 0 {
		t.Fatalf("expired certificate must fail before network I/O, but %d requests reached the server", gets)
	}
}
