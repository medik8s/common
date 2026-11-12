package nodeidentity

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// GetOwnNode fetches the caller's own Node, validates it against the
// configured node name and expected UID, and records its resourceVersion as
// the precondition for a subsequent PatchOwnNodeCondition.
func (c *Client) GetOwnNode(ctx context.Context) (*Node, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/v1/nodes/"+url.PathEscape(c.cfg.NodeName), "", nil)
	if err != nil {
		return nil, err
	}
	defer drainAndClose(resp)
	if err := classify(resp); err != nil {
		return nil, err
	}
	node, err := decodeNodeBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if err := c.validateOwn(node); err != nil {
		return nil, err
	}
	c.setObservedRV(node.Metadata.ResourceVersion)
	return node, nil
}

// validateOwn enforces the identity invariants on a decoded Node: the name
// must match, uid and resourceVersion must be present, and the live UID must
// match the UID recorded at enrollment. A Node object replaced behind the
// identity's back must never be acted on.
func (c *Client) validateOwn(n *Node) error {
	if n.Metadata.Name != c.cfg.NodeName {
		return fmt.Errorf("%w: server returned node %q for %q",
			ErrNodeNameMismatch, n.Metadata.Name, c.cfg.NodeName)
	}
	if n.Metadata.UID == "" || n.Metadata.ResourceVersion == "" {
		return fmt.Errorf("%w: response for %q is missing uid or resourceVersion",
			ErrAPIServer, c.cfg.NodeName)
	}
	if n.Metadata.UID != c.cfg.ExpectedUID {
		return fmt.Errorf("%w: live UID %q does not match expected %q; the Node object was replaced",
			ErrNodeNameMismatch, n.Metadata.UID, c.cfg.ExpectedUID)
	}
	return nil
}

// classify maps an HTTP status to a typed error. Success statuses are nil;
// redirects are errors (the client never follows them by construction).
func classify(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusNotFound:
		return ErrNodeNotFound
	case http.StatusConflict:
		return ErrConflict
	default:
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return fmt.Errorf("%w: unexpected redirect (HTTP %d) to %q",
				ErrAPIServer, resp.StatusCode, resp.Header.Get("Location"))
		}
		return fmt.Errorf("%w: HTTP %d", ErrAPIServer, resp.StatusCode)
	}
}
