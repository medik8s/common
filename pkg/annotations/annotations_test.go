package annotations

import "testing"

// The values are a wire contract between NHC, which stamps them, and the
// remediators that read them; a rename breaks deployed pairs silently.
func TestNodeIdentityAnnotationKeys(t *testing.T) {
	for name, tc := range map[string]struct{ got, want string }{
		"node UID":         {NodeUIDAnnotation, "remediation.medik8s.io/node-uid"},
		"node system UUID": {NodeSystemUUIDAnnotation, "remediation.medik8s.io/node-system-uuid"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s annotation key = %q, want %q", name, tc.got, tc.want)
		}
	}
}
