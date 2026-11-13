package nodeidentity_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/medik8s/common/pkg/nodeidentity"
)

// testPKI generates a throwaway CA and leaf certificates for tests.
type testPKI struct {
	dir       string
	caKey     *ecdsa.PrivateKey
	caDER     []byte
	caPEM     []byte
	caPool    *x509.CertPool
	serialSeq int64
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "nodeidentity-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, key.Public(), key)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("cannot add CA to pool")
	}
	return &testPKI{dir: dir, caKey: key, caDER: der, caPEM: caPEM, caPool: pool, serialSeq: 1}
}

func (p *testPKI) nextSerial() *big.Int {
	p.serialSeq++
	return big.NewInt(p.serialSeq)
}

// newLeaf issues a leaf certificate signed by the test CA.
func (p *testPKI) newLeaf(t *testing.T, cn string, notBefore, notAfter time.Time, eku x509.ExtKeyUsage, dnsNames []string, ips []net.IP) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber: p.nextSerial(),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{eku},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}
	caCert, err := x509.ParseCertificate(p.caDER)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, key.Public(), p.caKey)
	if err != nil {
		t.Fatalf("create leaf: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal leaf key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// writeClientPair issues a client leaf and writes cert/key files.
func (p *testPKI) writeClientPair(t *testing.T, cn string, notBefore, notAfter time.Time) (certFile, keyFile string) {
	t.Helper()
	certPEM, keyPEM := p.newLeaf(t, cn, notBefore, notAfter, x509.ExtKeyUsageClientAuth, nil, nil)
	return writePair(t, p.dir, cn, certPEM, keyPEM)
}

func writePair(t *testing.T, dir, name string, certPEM, keyPEM []byte) (string, string) {
	t.Helper()
	certFile := filepath.Join(dir, name+"-client.crt")
	keyFile := filepath.Join(dir, name+"-client.key")
	if err := os.WriteFile(certFile, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// writeCA writes the CA bundle to disk and returns its path.
func (p *testPKI) writeCA(t *testing.T) string {
	t.Helper()
	caFile := filepath.Join(p.dir, "ca.crt")
	if err := os.WriteFile(caFile, p.caPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return caFile
}

// newTestServer starts an httptest TLS server whose certificate is signed by
// the test CA (so the client can verify it) and which requires and verifies
// client certificates (so tests can assert which certificate was presented).
func (p *testPKI) newTestServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	certPEM, keyPEM := p.newLeaf(t, "server", time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour),
		x509.ExtKeyUsageServerAuth, []string{"127.0.0.1", "localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	serverCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("server keypair: %v", err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    p.caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	srv.EnableHTTP2 = true // the client must still negotiate HTTP/1.1 only
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// newTestClient builds a nodeidentity.Client against the test server.
func (p *testPKI) newTestClient(t *testing.T, serverURL, nodeName, uid, certFile, keyFile string, opts ...func(*nodeidentity.Config)) *nodeidentity.Client {
	t.Helper()
	cfg := nodeidentity.Config{
		NodeName:    nodeName,
		ExpectedUID: uid,
		APIServer:   serverURL,
		CAFile:      p.writeCA(t),
		CertFile:    certFile,
		KeyFile:     keyFile,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	c, err := nodeidentity.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// fakeNodeAPI is a stateful fake of the endpoints this package uses. It
// enforces the resourceVersion precondition on status patches (409 on
// mismatch), merges conditions by type (strategic-merge semantics), models
// the main-resource cordon patch and the NodeRestriction taint refusal, and
// records which client certificate CN was presented per request.
type fakeNodeAPI struct {
	mu          sync.Mutex
	name, uid   string
	rv          int
	conditions  []nodeidentity.Condition
	taints      []nodeidentity.Taint
	unsched     bool
	refuseTaint bool
	conflicts   int
	gets, patch int
	peerCNs     []string
	proto       string
	taintDryRun string
}

// mergeConditionLocked merges one incoming condition (as a raw field map)
// into the stored conditions by type, applying ONLY the fields present in
// the incoming JSON - Kubernetes strategic-merge semantics: a status-only
// update leaves the stored Reason untouched, and an explicitly empty value
// clears the field.
func (f *fakeNodeAPI) mergeConditionLocked(in map[string]json.RawMessage) {
	var condType string
	if raw, ok := in["type"]; ok {
		_ = json.Unmarshal(raw, &condType)
	}
	for i := range f.conditions {
		if f.conditions[i].Type != condType {
			continue
		}
		f.conditions[i] = mergeConditionFields(f.conditions[i], in)
		return
	}
	var fresh nodeidentity.Condition
	fresh.Type = condType
	f.conditions = append(f.conditions, mergeConditionFields(fresh, in))
}

// mergeConditionFields overlays the fields present in the incoming raw map
// onto the stored condition.
func mergeConditionFields(stored nodeidentity.Condition, in map[string]json.RawMessage) nodeidentity.Condition {
	if raw, ok := in["type"]; ok {
		_ = json.Unmarshal(raw, &stored.Type)
	}
	if raw, ok := in["status"]; ok {
		_ = json.Unmarshal(raw, &stored.Status)
	}
	if raw, ok := in["reason"]; ok {
		_ = json.Unmarshal(raw, &stored.Reason)
	}
	if raw, ok := in["message"]; ok {
		_ = json.Unmarshal(raw, &stored.Message)
	}
	if raw, ok := in["lastTransitionTime"]; ok {
		_ = json.Unmarshal(raw, &stored.LastTransitionTime)
	}
	return stored
}

func newFakeNodeAPI(name, uid string) *fakeNodeAPI {
	// The measured platforms refuse taint writes from the node identity
	// (NodeRestriction); the fake models that by default. Tests of the
	// unexpected-permitted path clear the flag.
	return &fakeNodeAPI{name: name, uid: uid, rv: 100, refuseTaint: true}
}

func (f *fakeNodeAPI) nodeJSON() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.marshalLocked()
}

func (f *fakeNodeAPI) marshalLocked() []byte {
	n := struct {
		Metadata struct {
			Name            string `json:"name"`
			UID             string `json:"uid"`
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Spec struct {
			Unschedulable bool                 `json:"unschedulable,omitempty"`
			Taints        []nodeidentity.Taint `json:"taints,omitempty"`
		} `json:"spec"`
		Status struct {
			Conditions []nodeidentity.Condition `json:"conditions"`
		} `json:"status"`
	}{}
	n.Metadata.Name = f.name
	n.Metadata.UID = f.uid
	n.Metadata.ResourceVersion = fmt.Sprintf("%d", f.rv)
	n.Spec.Unschedulable = f.unsched
	n.Spec.Taints = f.taints
	n.Status.Conditions = f.conditions
	b, _ := json.Marshal(n)
	return b
}

func (f *fakeNodeAPI) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		if len(r.TLS.PeerCertificates) > 0 {
			f.peerCNs = append(f.peerCNs, r.TLS.PeerCertificates[0].Subject.CommonName)
		}
		f.proto = r.Proto
		f.mu.Unlock()

		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/nodes/"):
			name := strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/")
			f.mu.Lock()
			f.gets++
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if name == f.name {
				_, _ = w.Write(f.nodeJSON())
				return
			}
			// Reads of other nodes are permitted to the node identity.
			fmt.Fprintf(w, `{"metadata":{"name":%q,"uid":"uid-%s","resourceVersion":"1"}}`, name, name)

		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/nodes/"+f.name+"/status":
			if r.Header.Get("Content-Type") == "application/json-patch+json" {
				if !f.applyJSONPatchStatus(w, r) {
					return
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				f.rv++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(f.marshalLocked())
				return
			}
			var p struct {
				Metadata struct {
					ResourceVersion string `json:"resourceVersion"`
					UID             string `json:"uid"`
				} `json:"metadata"`
				Status struct {
					Conditions []map[string]json.RawMessage `json:"conditions"`
				} `json:"status"`
			}
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			f.patch++
			// The real API server rejects a patch that would change the
			// immutable uid field, so a mismatching metadata.uid fails the
			// whole write with 422 before any status change.
			if p.Metadata.UID != "" && p.Metadata.UID != f.uid {
				w.WriteHeader(http.StatusUnprocessableEntity)
				fmt.Fprintf(w, `{"kind":"Status","status":"Failure","code":422,"message":"metadata.uid: Invalid value: %q: field is immutable"}`, p.Metadata.UID)
				return
			}
			if p.Metadata.ResourceVersion != fmt.Sprintf("%d", f.rv) {
				f.conflicts++
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"kind":"Status","status":"Failure","code":409,"reason":"Conflict"}`))
				return
			}
			for _, in := range p.Status.Conditions {
				f.mergeConditionLocked(in)
			}
			f.rv++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(f.marshalLocked())

		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/nodes/"+f.name:
			// Handle JSON Patch on the main resource (taint removal).
			if r.Header.Get("Content-Type") == "application/json-patch+json" {
				if !f.applyJSONPatchTaints(w, r) {
					return
				}
				f.mu.Lock()
				f.rv++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(f.marshalLocked())
				f.mu.Unlock()
				return
			}
			var p struct {
				Metadata struct {
					UID             string `json:"uid"`
					ResourceVersion string `json:"resourceVersion"`
				} `json:"metadata"`
				Spec struct {
					Unschedulable *bool `json:"unschedulable"`
					Taints        []struct {
						nodeidentity.Taint
						PatchDirective string `json:"$patch"`
					} `json:"taints"`
				} `json:"spec"`
			}
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			f.patch++
			// The real API server rejects a patch that would change the
			// immutable uid field, so a mismatching metadata.uid fails the
			// whole write with 422 before any spec change.
			if p.Metadata.UID != "" && p.Metadata.UID != f.uid {
				w.WriteHeader(http.StatusUnprocessableEntity)
				fmt.Fprintf(w, `{"kind":"Status","status":"Failure","code":422,"message":"metadata.uid: Invalid value: %q: field is immutable"}`, p.Metadata.UID)
				return
			}
			if p.Metadata.ResourceVersion != "" && p.Metadata.ResourceVersion != fmt.Sprint(f.rv) {
				w.WriteHeader(http.StatusConflict)
				return
			}
			if len(p.Spec.Taints) > 0 {
				// Record whether the taint probe requested dry-run
				// processing, so tests can assert the request shape.
				f.taintDryRun = r.URL.Query().Get("dryRun")
				if f.refuseTaint {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"kind":"Status","status":"Failure","code":403,"reason":"Forbidden","message":"node \"` + f.name + `\" is not allowed to modify taints"}`))
					return
				}
				// Node taints are atomic, not a merge-by-key list. Dry-run
				// returns the proposed object without persisting it or its RV.
				proposed := make([]nodeidentity.Taint, len(p.Spec.Taints))
				for i, taint := range p.Spec.Taints {
					proposed[i] = taint.Taint
				}
				if r.URL.Query().Get("dryRun") == "All" {
					saved := f.taints
					f.taints = proposed
					data := f.marshalLocked()
					f.taints = saved
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(data)
					return
				}
				f.taints = proposed
			}
			if p.Spec.Unschedulable != nil {
				f.unsched = *p.Spec.Unschedulable
			}
			f.rv++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(f.marshalLocked())

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// bumpRV simulates another writer changing the node between the client's GET
// and PATCH, so the next patch hits a stale resourceVersion precondition.
func (f *fakeNodeAPI) bumpRV() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rv++
}

func (f *fakeNodeAPI) conditionsOf(condType string) (nodeidentity.Condition, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conditions {
		if c.Type == condType {
			return c, true
		}
	}
	return nodeidentity.Condition{}, false
}

// applyJSONPatchTaints applies a JSON-Patch document to the main resource
// (spec.taints removal). It models the guarded test+remove pattern for
// taints, returning false when it has already answered.
func (f *fakeNodeAPI) applyJSONPatchTaints(w http.ResponseWriter, r *http.Request) bool {
	var ops []struct {
		Op    string          `json:"op"`
		Path  string          `json:"path"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&ops); err != nil || len(ops) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patch++
	for _, op := range ops {
		if op.Path == "/metadata/uid" {
			want, _ := json.Marshal(f.uid)
			if string(op.Value) != string(want) {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"kind":"Status","status":"Failure","code":409,"reason":"Conflict"}`))
				return false
			}
			continue
		}
		var idx int
		if _, err := fmt.Sscanf(op.Path, "/spec/taints/%d", &idx); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return false
		}
		if idx < 0 || idx >= len(f.taints) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"kind":"Status","status":"Failure","code":409,"reason":"Conflict"}`))
			return false
		}
		switch op.Op {
		case "test":
			want := string(op.Value)
			got, _ := json.Marshal(f.taints[idx].Key)
			if want != string(got) {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"kind":"Status","status":"Failure","code":409,"reason":"Conflict"}`))
				return false
			}
		case "remove":
			f.taints = append(f.taints[:idx], f.taints[idx+1:]...)
		default:
			w.WriteHeader(http.StatusBadRequest)
			return false
		}
	}
	return true
}

// applyJSONPatchStatus applies a JSON-Patch document to the status
// subresource. It implements the ops the package issues — test and remove on
// /status/conditions/N — and answers 409 when a test op fails (index shift)
// or 400 for anything it does not model, so an unexpected patch shape can
// never silently succeed. It returns false when it has already answered.
func (f *fakeNodeAPI) applyJSONPatchStatus(w http.ResponseWriter, r *http.Request) bool {
	var ops []struct {
		Op    string          `json:"op"`
		Path  string          `json:"path"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&ops); err != nil || len(ops) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patch++
	for _, op := range ops {
		if op.Path == "/metadata/uid" || op.Path == "/metadata/resourceVersion" {
			value := f.uid
			if op.Path == "/metadata/resourceVersion" {
				value = fmt.Sprint(f.rv)
			}
			want, _ := json.Marshal(value)
			if string(op.Value) != string(want) {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"kind":"Status","status":"Failure","code":409,"reason":"Conflict"}`))
				return false
			}
			continue
		}
		var idx int
		if _, err := fmt.Sscanf(op.Path, "/status/conditions/%d", &idx); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return false
		}
		if idx < 0 || idx >= len(f.conditions) {
			// A stale index answers as a failed guarded op, never a panic.
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"kind":"Status","status":"Failure","code":409,"reason":"Conflict"}`))
			return false
		}
		switch op.Op {
		case "test":
			want := string(op.Value)
			got, _ := json.Marshal(f.conditions[idx].Type)
			if want != string(got) {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"kind":"Status","status":"Failure","code":409,"reason":"Conflict"}`))
				return false
			}
		case "remove":
			f.conditions = append(f.conditions[:idx], f.conditions[idx+1:]...)
		default:
			w.WriteHeader(http.StatusBadRequest)
			return false
		}
	}
	return true
}

func (f *fakeNodeAPI) unschedulable() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unsched
}

func (f *fakeNodeAPI) taintOf(key string) (nodeidentity.Taint, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.taints {
		if t.Key == key {
			return t, true
		}
	}
	return nodeidentity.Taint{}, false
}
