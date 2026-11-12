// Command nodeidentity-probe measures what a node's own identity may do to
// its Node object: the permission matrix the pre-kubelet self-cordon design
// depends on. It is the executable form of the per-platform verification that
// design requires — run it once per supported OpenShift version, on the node
// itself or from any host that can read the node's credential files and reach
// the direct API endpoint.
//
// This binary is a DIAGNOSTIC TOOL for humans and CI, not a runtime
// component: the production consumers (the SBR boot unit, the agent, the
// conformance test) import pkg/nodeidentity and call the Client directly.
// Nothing ships this tool, nothing parses its output, and its exit codes are
// for shells and pipelines, not an API. See the README in this directory for
// when to run it — and when not to.
//
// Cleanup is best-effort and reported: confirmed cordons are restored when
// safely observable, owned conditions are removed, and taint writes are only
// dry-run requests. Use an exclusive qualification node. See pkg/nodeidentity
// for ambiguous writes and pre-existing cordons.
//
// Exit codes: 0 every check matched the measured platforms; 1 at least one
// deviation (including checks that could not be measured); 2 the probe could
// not run at all.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/medik8s/common/pkg/nodeidentity"
)

func main() {
	os.Exit(run())
}

func run() int {
	fs := flag.NewFlagSet("nodeidentity-probe", flag.ContinueOnError)
	var (
		nodeName   = fs.String("node-name", "", "name of the Node this identity belongs to (required)")
		uid        = fs.String("uid", "", "expected Node UID, recorded while the node was healthy (required)")
		apiServer  = fs.String("api-server", "", "direct host-routable API server endpoint, https only (required)")
		caFile     = fs.String("ca", "", "CA bundle path used to verify the API server (required)")
		certFile   = fs.String("cert", "", "kubelet client certificate path (required)")
		keyFile    = fs.String("key", "", "kubelet client key path (required)")
		identityIn = fs.String("identity-file", "", "identity descriptor JSON (same schema as NODEIDENTITY_IDENTITY_FILE); individual flags override it")
		otherNode  = fs.String("other-node", "", "also measure the read of this node (optional)")
		timeout    = fs.Duration("timeout", 60*time.Second, "timeout for checks (cleanup has separate bounded contexts)")
		jsonOut    = fs.Bool("json", false, "print the result as JSON")
	)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}

	cfg := nodeidentity.Config{
		NodeName:    *nodeName,
		ExpectedUID: *uid,
		APIServer:   *apiServer,
		CAFile:      *caFile,
		CertFile:    *certFile,
		KeyFile:     *keyFile,
	}
	if *identityIn != "" {
		overridden, err := loadIdentityFile(*identityIn)
		if err != nil {
			fmt.Fprintf(os.Stderr, "nodeidentity-probe: %v\n", err)
			return 2
		}
		cfg = overridden
	}
	applyOverrides(&cfg, fs)

	client, err := nodeidentity.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nodeidentity-probe: %v\n", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	res := client.RunPermissionMatrix(ctx, nodeidentity.ProbeOptions{
		OtherNode:          *otherNode,
		ConditionTimestamp: time.Now().UTC().Format(time.RFC3339),
	})

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
	} else {
		fmt.Printf("node identity permission matrix\n")
		fmt.Printf("node:    %s (uid %s)\n", res.NodeName, res.NodeUID)
		fmt.Printf("server:  %s\n\n", res.APIServer)
		fmt.Printf("%-44s %-10s %-10s %s\n", "check", "expected", "outcome", "detail")
		for _, chk := range res.Checks {
			fmt.Printf("%-44s %-10s %-10s %s\n", chk.Name, chk.Expected, chk.Outcome, chk.Detail)
		}
		fmt.Println()
	}

	if deviations := res.Unexpected(); len(deviations) > 0 {
		fmt.Fprintf(os.Stderr, "nodeidentity-probe: %d deviation(s)\n", len(deviations))
		return 1
	}
	if !*jsonOut {
		fmt.Println("all checks match the measured platforms")
	}
	return 0
}

// applyOverrides lets individual flags override the identity file: only a
// flag that was actually set replaces the file's value.
func applyOverrides(cfg *nodeidentity.Config, fs *flag.FlagSet) {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	lookup := func(name string) string {
		f := fs.Lookup(name)
		if f == nil {
			return ""
		}
		return f.Value.String()
	}
	if set["node-name"] {
		cfg.NodeName = lookup("node-name")
	}
	if set["uid"] {
		cfg.ExpectedUID = lookup("uid")
	}
	if set["api-server"] {
		cfg.APIServer = lookup("api-server")
	}
	if set["ca"] {
		cfg.CAFile = lookup("ca")
	}
	if set["cert"] {
		cfg.CertFile = lookup("cert")
	}
	if set["key"] {
		cfg.KeyFile = lookup("key")
	}
}

// identityFile mirrors the descriptor written by an enrollment routine
// (identity.json), the same schema the conformance test reads.
type identityFile struct {
	SchemaVersion  string `json:"schemaVersion"`
	NodeName       string `json:"nodeName"`
	NodeUID        string `json:"nodeUID"`
	APIServerURL   string `json:"apiServerURL"`
	CACertPath     string `json:"caCertPath"`
	ClientCertPath string `json:"clientCertPath"`
	ClientKeyPath  string `json:"clientKeyPath"`
}

func loadIdentityFile(path string) (nodeidentity.Config, error) {
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nodeidentity.Config{}, err
		}
		path = abs
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nodeidentity.Config{}, fmt.Errorf("read identity file: %w", err)
	}
	var d identityFile
	if err := json.Unmarshal(data, &d); err != nil {
		return nodeidentity.Config{}, fmt.Errorf("parse identity file %s: %w", path, err)
	}
	return nodeidentity.Config{
		NodeName:    d.NodeName,
		ExpectedUID: d.NodeUID,
		APIServer:   d.APIServerURL,
		CAFile:      d.CACertPath,
		CertFile:    d.ClientCertPath,
		KeyFile:     d.ClientKeyPath,
	}, nil
}
