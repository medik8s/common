package taints

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

var (
	loggerTaint = ctrl.Log.WithName("taints")
	//IsOutOfServiceTaintSupported will be set to true in case OutOfServiceTaint is supported (k8s 1.26 or higher)
	IsOutOfServiceTaintSupported bool
	//IsOutOfServiceTaintGA will be set to true in case OutOfServiceTaint is GA (k8s 1.28 or higher)
	IsOutOfServiceTaintGA bool
	leadingDigits         = regexp.MustCompile(`^(\d+)`)
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

// DeleteTaint removes all the taints that have the same key and effect to given taintToDelete.
func DeleteTaint(taints []corev1.Taint, taintToDelete *corev1.Taint) ([]corev1.Taint, bool) {
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

// CreateOutOfServiceTaint returns an OutOfService taint
func CreateOutOfServiceTaint() corev1.Taint {
	now := metav1.Now()
	return corev1.Taint{
		Key:       corev1.TaintNodeOutOfService,
		Value:     "nodeshutdown",
		Effect:    corev1.TaintEffectNoExecute,
		TimeAdded: &now,
	}
}

// InitOutOfServiceTaintFlagsWithRetry tries to initialize the OutOfService flags based on k8s version, in case it fails (potentially due to network issues) it will retry for a limited number of times
func InitOutOfServiceTaintFlagsWithRetry(ctx context.Context, config *rest.Config) error {

	var err error
	interval := 2 * time.Second // retry every 2 seconds
	timeout := 10 * time.Second // for a period of 10 seconds

	// Since the last internal error returned by InitOutOfServiceTaintFlags also indicates whether polling succeed or not, there is no need to also keep the context error returned by PollUntilContextTimeout.
	// Using wait.PollUntilContextTimeout to retry initOutOfServiceTaintFlags in case there is a temporary network issue.
	_ = wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(ctx context.Context) (bool, error) {
		if err = initOutOfServiceTaintFlags(config); err != nil {
			return false, nil // Keep retrying
		}
		return true, nil // Success
	})
	return err
}

func initOutOfServiceTaintFlags(config *rest.Config) error {
	if cs, err := kubernetes.NewForConfig(config); err != nil || cs == nil {
		if cs == nil {
			err = fmt.Errorf("k8s client set is nil")
		}
		loggerTaint.Error(err, "couldn't retrieve k8s client")
		return err
	} else if k8sVersion, err := cs.Discovery().ServerVersion(); err != nil || k8sVersion == nil {
		if k8sVersion == nil {
			err = fmt.Errorf("k8s server version is nil")
		}
		loggerTaint.Error(err, "couldn't retrieve k8s server version")
		return err
	} else {
		return setOutOfTaintFlags(k8sVersion)
	}
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

	IsOutOfServiceTaintSupported = majorVer > minK8sMajorVersionOutOfServiceTaint || (majorVer == minK8sMajorVersionOutOfServiceTaint && minorVer >= minK8sMinorVersionSupportingOutOfServiceTaint)
	loggerTaint.Info("out of service taint strategy", "isSupported", IsOutOfServiceTaintSupported, "k8sMajorVersion", majorVer, "k8sMinorVersion", minorVer)
	IsOutOfServiceTaintGA = majorVer > minK8sMajorVersionOutOfServiceTaint || (majorVer == minK8sMajorVersionOutOfServiceTaint && minorVer >= minK8sMinorVersionGAOutOfServiceTaint)
	loggerTaint.Info("out of service taint strategy", "isGA", IsOutOfServiceTaintGA, "k8sMajorVersion", majorVer, "k8sMinorVersion", minorVer)
	return nil
}
