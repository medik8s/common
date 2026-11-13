package nodeidentity_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/medik8s/common/pkg/nodeidentity"
)

func TestNewValidation(t *testing.T) {
	base := nodeidentity.Config{
		NodeName: "n", ExpectedUID: "u", APIServer: "https://x", CAFile: "c", CertFile: "crt", KeyFile: "key",
	}
	cases := []struct {
		name string
		mut  func(*nodeidentity.Config)
	}{
		{"missing NodeName", func(c *nodeidentity.Config) { c.NodeName = "" }},
		{"missing ExpectedUID", func(c *nodeidentity.Config) { c.ExpectedUID = "" }},
		{"missing APIServer", func(c *nodeidentity.Config) { c.APIServer = "" }},
		{"missing CAFile", func(c *nodeidentity.Config) { c.CAFile = "" }},
		{"missing CertFile", func(c *nodeidentity.Config) { c.CertFile = "" }},
		{"missing KeyFile", func(c *nodeidentity.Config) { c.KeyFile = "" }},
		{"http endpoint", func(c *nodeidentity.Config) { c.APIServer = "http://x" }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			tt.mut(&cfg)
			if _, err := nodeidentity.New(cfg); !errors.Is(err, nodeidentity.ErrInvalidConfig) {
				t.Fatalf("expected ErrInvalidConfig, got %v", err)
			}
		})
	}
	if _, err := nodeidentity.New(base); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestNewNormalizesAPIServer(t *testing.T) {
	cfg := nodeidentity.Config{
		NodeName: "n", ExpectedUID: "u", APIServer: "https://x/",
		CAFile: "c", CertFile: "crt", KeyFile: "key",
	}
	c, err := nodeidentity.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Config().APIServer; got != "https://x" {
		t.Fatalf("trailing slash not trimmed: %q", got)
	}
}

func TestGetOwnNodeStatusMatrix(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	cases := []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{"unauthorized", http.StatusUnauthorized, `{}`, nodeidentity.ErrUnauthorized},
		{"forbidden", http.StatusForbidden, `{}`, nodeidentity.ErrForbidden},
		{"not found", http.StatusNotFound, `{}`, nodeidentity.ErrNodeNotFound},
		{"conflict", http.StatusConflict, `{}`, nodeidentity.ErrConflict},
		{"server error", http.StatusInternalServerError, `{}`, nodeidentity.ErrAPIServer},
		{"redirect", http.StatusFound, `{}`, nodeidentity.ErrAPIServer},
		{"malformed json", http.StatusOK, `{not json`, nodeidentity.ErrAPIServer},
		{"empty body", http.StatusOK, ``, nodeidentity.ErrAPIServer},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://elsewhere.example")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			srv := pki.newTestServer(t, h)
			c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)
			_, err := c.GetOwnNode(context.Background())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestGetOwnNodeContextCancel(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	})
	srv := pki.newTestServer(t, h)
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := c.GetOwnNode(ctx); !errors.Is(err, nodeidentity.ErrAPIServer) {
		t.Fatalf("expected ErrAPIServer on context cancellation, got %v", err)
	}
}

func TestGetOwnNodeIdentityChecks(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	t.Run("name mismatch", func(t *testing.T) {
		api := newFakeNodeAPI("other-node", "uid-1")
		srv := pki.newTestServer(t, api.handler())
		c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)
		if _, err := c.GetOwnNode(context.Background()); !errors.Is(err, nodeidentity.ErrNodeNameMismatch) {
			t.Fatalf("expected ErrNodeNameMismatch, got %v", err)
		}
	})

	t.Run("uid mismatch (replaced node object)", func(t *testing.T) {
		api := newFakeNodeAPI("worker-1", "uid-replaced")
		srv := pki.newTestServer(t, api.handler())
		c := pki.newTestClient(t, srv.URL, "worker-1", "uid-expected", certFile, keyFile)
		if _, err := c.GetOwnNode(context.Background()); !errors.Is(err, nodeidentity.ErrNodeNameMismatch) {
			t.Fatalf("expected ErrNodeNameMismatch, got %v", err)
		}
	})

	t.Run("missing uid in response", func(t *testing.T) {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"metadata":{"name":"worker-1","resourceVersion":"5"}}`))
		})
		srv := pki.newTestServer(t, h)
		c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)
		if _, err := c.GetOwnNode(context.Background()); !errors.Is(err, nodeidentity.ErrAPIServer) {
			t.Fatalf("expected ErrAPIServer for missing uid, got %v", err)
		}
	})
}
