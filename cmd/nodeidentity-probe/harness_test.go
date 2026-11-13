package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the actual make recipe with local tools standing in for oc/go.
// This checks staging isolation, arguments and cleanup after copy/probe errors;
// it does not claim to qualify remote ownership, SELinux or oc debug behavior.
func TestHarnessPrivateStagingAndCleanup(t *testing.T) {
	for _, scenario := range []string{"success", "copy-failure", "probe-failure"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "oc.log")
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			write("go", `#!/bin/bash
set -eu
while [ "$1" != -o ]; do shift; done
printf '#!/bin/sh\nexit 0\n' > "$2"
`)
			write("oc", `#!/bin/bash
set -eu
printf '%s\n' "$*" >> "$PROBE_TEST_LOG"
case "$1" in
 project) echo default;;
 get)
  if [ "$2" = pod ]; then echo Running
  elif [[ "$*" == *architecture* ]]; then echo amd64
  else echo uid-1; fi;;
 debug) echo 'Starting pod/node-debug-test ...';;
 cp)
  [[ "$*" == *'/host/var/tmp/nodeidentity-probe.ABCDE12345/nodeidentity-probe' ]]
  if [ "$PROBE_TEST_SCENARIO" = copy-failure ]; then exit 1; fi;;
 exec)
  if [[ "$*" == *'mktemp -d'* ]]; then
    [[ "$*" == *'umask 077'* ]]
    echo /var/tmp/nodeidentity-probe.ABCDE12345
  elif [[ "$*" == *'/bin/rm -rf --'* ]]; then
    [[ "$*" == *'/var/tmp/nodeidentity-probe.ABCDE12345' ]]
  else
    [[ "${!#}" = /var/tmp/nodeidentity-probe.ABCDE12345 ]]
    if [ "$PROBE_TEST_SCENARIO" = probe-failure ]; then exit 1; fi
  fi;;
 delete) :;;
 *) exit 99;;
esac
`)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "make", "--no-print-directory", "nodeidentity-probe", "NODEIDENTITY_PROBE_NODE=worker-1")
			cmd.Dir = filepath.Join("..", "..")
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "PROBE_TEST_LOG="+log, "PROBE_TEST_SCENARIO="+scenario)
			out, err := cmd.CombinedOutput()
			if scenario == "success" && err != nil {
				t.Fatalf("recipe failed: %v\n%s", err, out)
			}
			if scenario != "success" {
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 2 {
					t.Fatalf("make must return 2 for failed recipe: %v\n%s", err, out)
				}
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			lines := string(data)
			cleanup := strings.Index(lines, "/bin/rm -rf -- /var/tmp/nodeidentity-probe.ABCDE12345")
			deletion := strings.Index(lines, "delete pod node-debug-test")
			if cleanup < 0 || deletion < cleanup {
				t.Fatalf("private directory must be cleaned before deleting debug pod:\n%s", lines)
			}
			if strings.Contains(lines, "/host/var/tmp/nodeidentity-probe ") {
				t.Fatal("shared staging path used")
			}
		})
	}
}
