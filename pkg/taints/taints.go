package taints

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	//out of service taint strategy const (supported from 1.26)
	minK8sMajorVersionOutOfServiceTaint           = 1
	minK8sMinorVersionSupportingOutOfServiceTaint = 26

	//out of service taint strategy const (GA from 1.28)
	minK8sMinorVersionGAOutOfServiceTaint = 28
)

// OutOfServiceTaintInfo contains information about out-of-service taint support in the cluster
type OutOfServiceTaintInfo struct {
	Supported bool // true if k8s version >= 1.26
	GA        bool // true if k8s version >= 1.28
}

var (
	loggerTaint   = ctrl.Log.WithName("taints")
	leadingDigits = regexp.MustCompile(`^(\d+)`)

	taintInfo OutOfServiceTaintInfo
)

// TaintExists checks if the given taint exists in list of taints. Returns true if exists false otherwise.
func TaintExists(taints []corev1.Taint, taintToFind *corev1.Taint) bool {
	for _, taint := range taints {
		if taint.MatchTaint(taintToFind) {
			return true
		}
	}
	return false
}

// FilterOutTaint returns a new taint slice without taints matching the given taintToDelete by key and effect.
// Also returns true if any taints were filtered out, false otherwise.
func FilterOutTaint(taints []corev1.Taint, taintToDelete *corev1.Taint) ([]corev1.Taint, bool) {
	var newTaints []corev1.Taint
	deleted := false
	for i := range taints {
		if taintToDelete.MatchTaint(&taints[i]) {
			deleted = true
			continue
		}
		newTaints = append(newTaints, taints[i])
	}
	return newTaints, deleted
}

// CreateOutOfServiceTaint returns an OutOfService taint.
// TimeAdded is not set - caller should set it when applying the taint to ensure accurate timestamp.
func CreateOutOfServiceTaint() corev1.Taint {
	return corev1.Taint{
		Key:    corev1.TaintNodeOutOfService,
		Value:  "nodeshutdown",
		Effect: corev1.TaintEffectNoExecute,
	}
}

// GetOutOfServiceTaintInfo returns the out-of-service taint information.
// Must call InitOutOfServiceTaintFlagsWithRetry first to initialize.
func GetOutOfServiceTaintInfo() OutOfServiceTaintInfo {
	return taintInfo
}

// InitOutOfServiceTaintFlagsWithRetry tries to initialize the OutOfService taint info based on k8s version, in case it fails (potentially due to network issues) it will retry for a limited number of times.
func InitOutOfServiceTaintFlagsWithRetry(ctx context.Context, config *rest.Config) error {
	var err error
	interval := 2 * time.Second // retry every 2 seconds
	timeout := 10 * time.Second // for a period of 10 seconds

	// Using wait.PollUntilContextTimeout to retry initOutOfServiceTaintFlags in case there is a temporary network issue.
	pollErr := wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(ctx context.Context) (bool, error) {
		if err = initOutOfServiceTaintFlags(config); err != nil {
			return false, nil // Keep retrying
		}
		return true, nil // Success
	})

	// Respect context cancellation - return poll error so caller knows operation was cancelled
	if pollErr != nil && (errors.Is(pollErr, context.Canceled) || errors.Is(pollErr, context.DeadlineExceeded)) {
		return pollErr
	}
	// Return internal error: nil on success, or last failure on timeout (more specific than generic timeout)
	return err
}

func initOutOfServiceTaintFlags(config *rest.Config) error {
	cs, err := kubernetes.NewForConfig(config)
	if err != nil || cs == nil {
		if err == nil {
			err = fmt.Errorf("k8s client set is nil")
		}
		loggerTaint.Error(err, "couldn't retrieve k8s client")
		return err
	}

	k8sVersion, err := cs.Discovery().ServerVersion()
	if err != nil || k8sVersion == nil {
		if err == nil {
			err = fmt.Errorf("k8s server version is nil")
		}
		loggerTaint.Error(err, "couldn't retrieve k8s server version")
		return err
	}

	return setOutOfTaintFlags(k8sVersion)
}

func setOutOfTaintFlags(version *version.Info) error {
	var majorVer, minorVer int
	var err error
	if majorVer, err = strconv.Atoi(version.Major); err != nil {
		loggerTaint.Error(err, "couldn't parse k8s major version", "major version", version.Major)
		return err
	}
	if minorVer, err = strconv.Atoi(leadingDigits.FindString(version.Minor)); err != nil {
		loggerTaint.Error(err, "couldn't parse k8s minor version", "minor version", version.Minor)
		return err
	}

	taintInfo.Supported = majorVer > minK8sMajorVersionOutOfServiceTaint || (majorVer == minK8sMajorVersionOutOfServiceTaint && minorVer >= minK8sMinorVersionSupportingOutOfServiceTaint)
	loggerTaint.Info("out of service taint strategy", "isSupported", taintInfo.Supported, "k8sMajorVersion", majorVer, "k8sMinorVersion", minorVer)
	taintInfo.GA = majorVer > minK8sMajorVersionOutOfServiceTaint || (majorVer == minK8sMajorVersionOutOfServiceTaint && minorVer >= minK8sMinorVersionGAOutOfServiceTaint)
	loggerTaint.Info("out of service taint strategy", "isGA", taintInfo.GA, "k8sMajorVersion", majorVer, "k8sMinorVersion", minorVer)
	return nil
}
