package nodeidentity_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/medik8s/common/pkg/nodeidentity"
)

func TestNewTransportCAErrors(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	dir := t.TempDir()

	cfg := nodeidentity.Config{
		NodeName: "n", ExpectedUID: "u", APIServer: "https://x",
		CAFile: filepath.Join(dir, "missing-ca.crt"), CertFile: certFile, KeyFile: keyFile,
	}
	if _, err := nodeidentity.NewTransport(cfg); !errors.Is(err, nodeidentity.ErrCAUnavailable) {
		t.Fatalf("missing CA file: %v", err)
	}

	garbage := filepath.Join(dir, "garbage-ca.crt")
	if err := os.WriteFile(garbage, []byte("not a pem"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.CAFile = garbage
	if _, err := nodeidentity.NewTransport(cfg); !errors.Is(err, nodeidentity.ErrCAUnavailable) {
		t.Fatalf("garbage CA file: %v", err)
	}
}

func TestEnsureTransportSurfacesCAError(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	api := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, api.handler())

	// Valid CA path is replaced after construction: the error must surface on
	// first use, not at construction (New does not touch the filesystem).
	caFile := pki.writeCA(t)
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)
	_ = caFile
	os.Remove(caFile) // force the lazy CA load to fail
	if _, err := c.GetOwnNode(context.Background()); !errors.Is(err, nodeidentity.ErrCAUnavailable) {
		t.Fatalf("expected ErrCAUnavailable on first use, got %v", err)
	}
}

func TestConfigNowInjection(t *testing.T) {
	pki := newTestPKI(t)
	// The pair is valid "now", but the injected clock sits in the future
	// window before NotBefore of a not-yet-valid pair.
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	api := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, api.handler())

	future := func() time.Time { return time.Now().Add(30 * time.Minute) }
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile,
		func(cfg *nodeidentity.Config) { cfg.Now = future })

	if _, err := c.GetOwnNode(context.Background()); !errors.Is(err, nodeidentity.ErrCertNotYetValid) {
		t.Fatalf("injected clock must drive the validity check, got %v", err)
	}
}

func TestPatchOwnNodeStatusDecodeAndValidateErrors(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	t.Run("malformed patch response", func(t *testing.T) {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{broken`))
		})
		srv := pki.newTestServer(t, h)
		c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)
		if _, err := c.PatchOwnNodeStatus(context.Background(), nodeidentity.PatchTypeStrategicMerge, []byte(`{}`)); !errors.Is(err, nodeidentity.ErrAPIServer) {
			t.Fatalf("expected ErrAPIServer for undecodable patch response, got %v", err)
		}
	})

	t.Run("uid mismatch in patch response", func(t *testing.T) {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"metadata":{"name":"worker-1","uid":"uid-replaced","resourceVersion":"5"}}`))
		})
		srv := pki.newTestServer(t, h)
		c := pki.newTestClient(t, srv.URL, "worker-1", "uid-expected", certFile, keyFile)
		if _, err := c.PatchOwnNodeStatus(context.Background(), nodeidentity.PatchTypeStrategicMerge, []byte(`{}`)); !errors.Is(err, nodeidentity.ErrNodeNameMismatch) {
			t.Fatalf("expected ErrNodeNameMismatch, got %v", err)
		}
	})
}

func TestDecodeNodeBodyOversize(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"metadata":{"name":"worker-1","uid":"uid-1","resourceVersion":"5"},"padding":"` +
			strings.Repeat("x", 5<<20) + `"}`))
	})
	srv := pki.newTestServer(t, h)
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)

	if _, err := c.GetOwnNode(context.Background()); !errors.Is(err, nodeidentity.ErrAPIServer) {
		t.Fatalf("expected ErrAPIServer for oversized body, got %v", err)
	}
}
