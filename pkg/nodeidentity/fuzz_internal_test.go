package nodeidentity

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// FuzzParseClientPEM feeds arbitrary bytes into the PEM parser: it must never
// panic, and a nil error must imply a usable pair.
func FuzzParseClientPEM(f *testing.F) {
	f.Add([]byte("-----BEGIN CERTIFICATE-----\nZm9v\n-----END CERTIFICATE-----\n"),
		[]byte("-----BEGIN EC PRIVATE KEY-----\nZm9v\n-----END EC PRIVATE KEY-----\n"),
		int64(0))
	f.Fuzz(func(t *testing.T, certPEM, keyPEM []byte, nowUnix int64) {
		pair, err := parseClientPEM(certPEM, keyPEM, time.Unix(nowUnix, 0).UTC())
		if err == nil && (pair == nil || pair.Leaf == nil) {
			t.Fatal("nil error must imply a parsed pair with a leaf")
		}
	})
}

// FuzzDecodeNode feeds arbitrary bytes into the Node decoder: it must never
// panic, and a nil error must imply a decoded document.
func FuzzDecodeNode(f *testing.F) {
	f.Add([]byte(`{"metadata":{"name":"n","uid":"u","resourceVersion":"1"}}`))
	f.Add([]byte(`{"status":{"conditions":[{"type":"T","status":"True"}]}}`))
	f.Add([]byte(`not json`))
	f.Fuzz(func(t *testing.T, data []byte) {
		node, err := decodeNode(data)
		if err == nil && node == nil {
			t.Fatal("nil error must imply a decoded node")
		}
	})
}

// FuzzBuildConditionPatch feeds arbitrary field values into the patch builder:
// invalid UTF-8 must be rejected with a typed error; everything else must
// produce valid JSON that unmarshals back to exactly one condition carrying
// the same fields.
func FuzzBuildConditionBody(f *testing.F) {
	f.Add("1", "uid-1", "T", "True", "reason", "message", "2026-01-01T00:00:00Z")
	f.Fuzz(func(t *testing.T, rv, uid, ctype, cstatus, reason, message, ltt string) {
		cond := Condition{Type: ctype, Status: cstatus, Reason: reason, Message: message, LastTransitionTime: ltt}
		body, err := buildConditionPatch(rv, uid, cond)
		if err != nil {
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("only ErrInvalidConfig is expected, got %v", err)
			}
			return
		}
		var p conditionPatch
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatalf("output is not valid JSON: %v", err)
		}
		if p.Metadata.ResourceVersion != rv {
			t.Fatal("resourceVersion not preserved")
		}
		if len(p.Status.Conditions) != 1 {
			t.Fatalf("expected exactly one condition, got %d", len(p.Status.Conditions))
		}
		got := p.Status.Conditions[0]
		if got.Type != cond.Type || got.Status != cond.Status || got.Reason != cond.Reason ||
			got.Message != cond.Message || got.LastTransitionTime != cond.LastTransitionTime {
			t.Fatal("condition fields not preserved through the round trip")
		}
	})
}
