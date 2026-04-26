package lease

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var NowTime = metav1.NowMicro()

const (
	leaseHolderIdentity = "some-operator"
	leaseDuration       = 3600 * time.Second
	leaseNamespace      = "medik8s-leases"
)

type action string

var (
	requestLease    action = "request lease"
	invalidateLease action = "invalidate lease"
)

func getMockNode() *corev1.Node {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "miau",
			UID:  "foobar",
		},
	}
	return node
}

func getMockPod() *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "miau",
			UID:  "foobar",
		},
	}
	return pod
}

var _ = Describe("Leases", func() {

	// if current time is after this time, the lease is expired
	leaseExpiredTime := NowTime.Add(-leaseDuration).Add(-1 * time.Second)
	// if lease expires after this time, it should be renewed
	renewTriggerTime := NowTime

	DescribeTable("Updates",
		func(initialLease *coordv1.Lease, expectedLease *coordv1.Lease, actionOnLease action, expectedError error) {
			//Test Create lease
			if initialLease == nil {
				testCreateLease()
				return
			}

			node := getMockNode()
			objs := []runtime.Object{
				initialLease,
			}
			cl := fake.NewClientBuilder().WithRuntimeObjects(objs...).Build()
			manager, _ := NewManager(cl, leaseHolderIdentity)
			_, err := manager.GetLease(context.TODO(), node)
			Expect(err).NotTo(HaveOccurred())

			switch actionOnLease {
			case requestLease:
				err = manager.RequestLease(context.Background(), node, leaseDuration)
			case invalidateLease:
				manager, _ := NewManager(cl, "different-owner")
				err = manager.InvalidateLease(context.Background(), node)
			}

			if expectedLease == nil {
				Expect(err).To(HaveOccurred())
				Expect(err).To(Equal(expectedError))
			} else {
				Expect(err).NotTo(HaveOccurred())
				Expect(expectedError).NotTo(HaveOccurred())

				actualLease, err := manager.GetLease(context.Background(), node)
				Expect(err).NotTo(HaveOccurred())

				compareLeases(expectedLease, actualLease)

				err = manager.InvalidateLease(context.Background(), node)
				Expect(err).NotTo(HaveOccurred())

				actualLease, err = manager.GetLease(context.Background(), node)
				Expect(actualLease).To(BeNil())
				Expect(errors.IsNotFound(err)).To(BeTrue())

			}
		},

		Entry("fail to update valid lease with different holder identity",
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "v1",
							Kind:       "Node",
							Name:       "@",
							UID:        "#",
						},
					},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String("miau"),
					LeaseDurationSeconds: pointer.Int32(32000),
					AcquireTime:          nil,
					RenewTime:            &metav1.MicroTime{Time: NowTime.Add(-1 * time.Second)},
					LeaseTransitions:     nil,
				},
			},
			nil,
			requestLease,
			AlreadyHeldError{holderIdentity: "miau"},
		),
		Entry("update lease with different holder identity (full init)",
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "v1",
							Kind:       "Node",
							Name:       "@",
							UID:        "#",
						},
					},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String("miau"),
					LeaseDurationSeconds: pointer.Int32(44),
					AcquireTime:          &metav1.MicroTime{Time: time.Unix(42, 0)},
					RenewTime:            &metav1.MicroTime{Time: time.Unix(43, 0)},
					LeaseTransitions:     nil,
				},
			},
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: corev1.SchemeGroupVersion.WithKind("Node").Version,
						Kind:       corev1.SchemeGroupVersion.WithKind("Node").Kind,
						Name:       getMockNode().Name,
						UID:        getMockNode().UID,
					}},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String(leaseHolderIdentity),
					LeaseDurationSeconds: pointer.Int32(int32(leaseDuration.Seconds())),
					AcquireTime:          &NowTime,
					RenewTime:            &NowTime,
					LeaseTransitions:     pointer.Int32(1),
				},
			},
			requestLease,
			nil,
		),
		Entry("update expired lease with different holder identity (with transition update)",
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "v1",
							Kind:       "Node",
							Name:       "@",
							UID:        "#",
						},
					},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String("miau"),
					LeaseDurationSeconds: pointer.Int32(44),
					AcquireTime:          &metav1.MicroTime{Time: time.Unix(42, 0)},
					RenewTime:            &metav1.MicroTime{Time: time.Unix(43, 0)},
					LeaseTransitions:     pointer.Int32(3),
				},
			},
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: corev1.SchemeGroupVersion.WithKind("Node").Version,
						Kind:       corev1.SchemeGroupVersion.WithKind("Node").Kind,
						Name:       getMockNode().Name,
						UID:        getMockNode().UID,
					}},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String(leaseHolderIdentity),
					LeaseDurationSeconds: pointer.Int32(int32(leaseDuration.Seconds())),
					AcquireTime:          &NowTime,
					RenewTime:            &NowTime,
					LeaseTransitions:     pointer.Int32(4),
				},
			},
			requestLease,
			nil,
		),
		Entry("extend lease if same holder and zero duration and renew time (invalid lease)",
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "v1",
							Kind:       "Node",
							Name:       "@",
							UID:        "#",
						},
					},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String(leaseHolderIdentity),
					LeaseDurationSeconds: nil,
					AcquireTime:          &metav1.MicroTime{Time: NowTime.Add(-599 * time.Second)},
					RenewTime:            nil,
					LeaseTransitions:     pointer.Int32(3),
				},
			},
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: corev1.SchemeGroupVersion.WithKind("Node").Version,
						Kind:       corev1.SchemeGroupVersion.WithKind("Node").Kind,
						Name:       getMockNode().Name,
						UID:        getMockNode().UID,
					}},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String(leaseHolderIdentity),
					LeaseDurationSeconds: pointer.Int32(int32(leaseDuration.Seconds())),
					AcquireTime:          &NowTime,
					RenewTime:            &NowTime,
					LeaseTransitions:     pointer.Int32(4),
				},
			},
			requestLease,
			nil,
		),
		Entry("update lease if same holder and expired lease - check modified lease duration",
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "v1",
							Kind:       "Node",
							Name:       "@",
							UID:        "#",
						},
					},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String(leaseHolderIdentity),
					LeaseDurationSeconds: pointer.Int32(int32(leaseDuration.Seconds() - 42)),
					AcquireTime:          nil,
					RenewTime:            &metav1.MicroTime{Time: leaseExpiredTime},
					LeaseTransitions:     nil,
				},
			},
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: corev1.SchemeGroupVersion.WithKind("Node").Version,
						Kind:       corev1.SchemeGroupVersion.WithKind("Node").Kind,
						Name:       getMockNode().Name,
						UID:        getMockNode().UID,
					}},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String(leaseHolderIdentity),
					LeaseDurationSeconds: pointer.Int32(int32(leaseDuration.Seconds())),
					AcquireTime:          &NowTime,
					RenewTime:            &NowTime,
					LeaseTransitions:     pointer.Int32(1),
				},
			},
			requestLease,
			nil,
		),
		Entry("extend lease if same holder and expired lease (acquire time previously not nil)",
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "v1",
							Kind:       "Node",
							Name:       "@",
							UID:        "#",
						},
					},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String(leaseHolderIdentity),
					LeaseDurationSeconds: pointer.Int32(int32(leaseDuration.Seconds())),
					AcquireTime:          &metav1.MicroTime{Time: leaseExpiredTime},
					RenewTime:            &metav1.MicroTime{Time: leaseExpiredTime},
					LeaseTransitions:     pointer.Int32(1),
				},
			},
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: corev1.SchemeGroupVersion.WithKind("Node").Version,
						Kind:       corev1.SchemeGroupVersion.WithKind("Node").Kind,
						Name:       getMockNode().Name,
						UID:        getMockNode().UID,
					}},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String(leaseHolderIdentity),
					LeaseDurationSeconds: pointer.Int32(int32(leaseDuration.Seconds())),
					AcquireTime:          &metav1.MicroTime{Time: leaseExpiredTime},
					RenewTime:            &NowTime,
					LeaseTransitions:     pointer.Int32(1),
				},
			},
			requestLease,
			nil,
		),
		// TODO why is not setting aquire time and transitions?
		Entry("extend lease if same holder and expired lease (acquire time previously nil)",
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "v1",
							Kind:       "Node",
							Name:       "@",
							UID:        "#",
						},
					},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String(leaseHolderIdentity),
					LeaseDurationSeconds: pointer.Int32(int32(leaseDuration.Seconds())),
					AcquireTime:          nil,
					RenewTime:            &metav1.MicroTime{Time: leaseExpiredTime},
					LeaseTransitions:     nil,
				},
			},
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: corev1.SchemeGroupVersion.WithKind("Node").Version,
						Kind:       corev1.SchemeGroupVersion.WithKind("Node").Kind,
						Name:       getMockNode().Name,
						UID:        getMockNode().UID,
					}},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String(leaseHolderIdentity),
					LeaseDurationSeconds: pointer.Int32(int32(leaseDuration.Seconds())),
					AcquireTime:          &NowTime,
					RenewTime:            &NowTime,
					LeaseTransitions:     pointer.Int32(1),
				},
			},
			requestLease,
			nil,
		),
		// TODO why not setting aquire time and transitions?
		Entry("extend lease if same holder and lease will expire before current Time + two times the drainer timeout",
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: corev1.SchemeGroupVersion.WithKind("Node").Version,
							Kind:       corev1.SchemeGroupVersion.WithKind("Node").Kind,
							Name:       getMockNode().Name,
							UID:        getMockNode().UID,
						},
					},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String(leaseHolderIdentity),
					LeaseDurationSeconds: pointer.Int32(int32(leaseDuration.Seconds())),
					AcquireTime:          nil,
					RenewTime:            &metav1.MicroTime{Time: renewTriggerTime.Add(-1 * time.Second)},
					LeaseTransitions:     nil,
				},
			},
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: corev1.SchemeGroupVersion.WithKind("Node").Version,
						Kind:       corev1.SchemeGroupVersion.WithKind("Node").Kind,
						Name:       getMockNode().Name,
						UID:        getMockNode().UID,
					}},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String(leaseHolderIdentity),
					LeaseDurationSeconds: pointer.Int32(int32(leaseDuration.Seconds())),
					AcquireTime:          nil,
					RenewTime:            &NowTime,
					LeaseTransitions:     nil,
				},
			},
			requestLease,
			nil,
		),
		// TODO why not setting aquire time and transitions?
		Entry("dont extend lease if same holder and lease not about to expire before current Time + two times the drainertimeout",
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "v1",
							Kind:       "Node",
							Name:       "@",
							UID:        "#",
						},
					},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String(leaseHolderIdentity),
					LeaseDurationSeconds: pointer.Int32(int32(leaseDuration.Seconds())),
					AcquireTime:          nil,
					RenewTime:            &metav1.MicroTime{Time: renewTriggerTime.Add(time.Second)},
					LeaseTransitions:     nil,
				},
			},
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: "v1",
						Kind:       "Node",
						Name:       "@",
						UID:        "#",
					}},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String(leaseHolderIdentity),
					LeaseDurationSeconds: pointer.Int32(int32(leaseDuration.Seconds())),
					AcquireTime:          nil,
					RenewTime:            &metav1.MicroTime{Time: renewTriggerTime.Add(time.Second)},
					LeaseTransitions:     nil,
				},
			},
			requestLease,
			nil,
		),

		Entry("create new lease",
			nil,
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: "v1",
						Kind:       "Node",
						Name:       "@",
						UID:        "#",
					}},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String(leaseHolderIdentity),
					LeaseDurationSeconds: pointer.Int32(int32(leaseDuration.Seconds())),
					AcquireTime:          nil,
					RenewTime:            &metav1.MicroTime{Time: renewTriggerTime.Add(time.Second)},
					LeaseTransitions:     nil,
				},
			},
			requestLease,
			nil,
		),

		Entry("try to delete lease not owned",
			&coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node-miau",
					Namespace: leaseNamespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "v1",
							Kind:       "Node",
							Name:       "@",
							UID:        "#",
						},
					},
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       pointer.String("miau"),
					LeaseDurationSeconds: pointer.Int32(44),
					AcquireTime:          &metav1.MicroTime{Time: time.Now()},
					RenewTime:            &metav1.MicroTime{Time: time.Now()},
					LeaseTransitions:     nil,
				},
			},
			nil,
			invalidateLease,
			AlreadyHeldError{holderIdentity: "miau"},
		),
	)
})

func ExpectEqualWithNil(actual, expected interface{}, description string) {
	// heads up: interfaces representing a pointer are not nil when the pointer is nil
	if expected == nil || (reflect.ValueOf(expected).Kind() == reflect.Ptr && reflect.ValueOf(expected).IsNil()) {
		// BeNil() handles pointers correctly
		ExpectWithOffset(1, actual).To(BeNil(), description)
	} else {
		// compare unix time, precision of MicroTime is sometimes different
		if e, ok := expected.(*metav1.MicroTime); ok {
			expected = e.Unix()
			if actual != nil && reflect.ValueOf(actual).Kind() == reflect.Ptr && !reflect.ValueOf(actual).IsNil() {
				actual = actual.(*metav1.MicroTime).Unix()
			}
		}
		ExpectWithOffset(1, actual).To(Equal(expected), description)
	}
}

func compareLeases(expectedLease, actualLease *coordv1.Lease) {
	Expect(len(actualLease.ObjectMeta.OwnerReferences)).To(Equal(1))
	Expect(len(expectedLease.ObjectMeta.OwnerReferences)).To(Equal(1))

	actualLeaseOwner := actualLease.ObjectMeta.OwnerReferences[0]
	expectedLeaseOwner := expectedLease.ObjectMeta.OwnerReferences[0]

	Expect(actualLeaseOwner.APIVersion).To(Equal(expectedLeaseOwner.APIVersion))
	Expect(actualLeaseOwner.Kind).To(Equal(expectedLeaseOwner.Kind))
	Expect(actualLeaseOwner.Name).To(Equal(expectedLeaseOwner.Name))
	Expect(actualLeaseOwner.UID).To(Equal(expectedLeaseOwner.UID))

	ExpectEqualWithNil(actualLease.Spec.HolderIdentity, expectedLease.Spec.HolderIdentity, "holder identity should match")
	ExpectEqualWithNil(actualLease.Spec.RenewTime, expectedLease.Spec.RenewTime, "renew time should match")
	ExpectEqualWithNil(actualLease.Spec.AcquireTime, expectedLease.Spec.AcquireTime, "acquire time should match")
	ExpectEqualWithNil(actualLease.Spec.LeaseDurationSeconds, expectedLease.Spec.LeaseDurationSeconds, "actualLease duration should match")
	ExpectEqualWithNil(actualLease.Spec.LeaseTransitions, expectedLease.Spec.LeaseTransitions, "actualLease transitions should match")

	Expect(actualLease.Name).To(Equal(expectedLease.Name))

}

func generateExpectedLease(obj client.Object, kind string) *coordv1.Lease {
	return &coordv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", strings.ToLower(kind), obj.GetName()),
			Namespace: leaseNamespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "v1",
					Kind:       kind,
					Name:       obj.GetName(),
					UID:        obj.GetUID(),
				},
			},
		},
		Spec: coordv1.LeaseSpec{
			HolderIdentity:       pointer.String(leaseHolderIdentity),
			LeaseDurationSeconds: pointer.Int32(int32(leaseDuration.Seconds())),
			AcquireTime:          &metav1.MicroTime{Time: NowTime.Time},
			RenewTime:            &metav1.MicroTime{Time: NowTime.Time},
			LeaseTransitions:     pointer.Int32(0),
		},
	}
}

func testCreateLease() {
	node := getMockNode()
	cl := fake.NewClientBuilder().Build()
	manager, _ := NewManager(cl, leaseHolderIdentity)
	_, err := manager.GetLease(context.Background(), node)
	Expect(err.Error()).To(ContainSubstring("not found"))
	err = manager.RequestLease(context.Background(), node, leaseDuration)
	Expect(err).NotTo(HaveOccurred())
	actualLease, err := manager.GetLease(context.Background(), node)
	Expect(err).ToNot(HaveOccurred())
	compareLeases(generateExpectedLease(node, "Node"), actualLease)

	pod := getMockPod()
	_, err = manager.GetLease(context.Background(), pod)
	Expect(err.Error()).To(ContainSubstring("not found"))
	err = manager.RequestLease(context.Background(), pod, leaseDuration)
	Expect(err).NotTo(HaveOccurred())
	actualLeaseFromPod, err := manager.GetLease(context.Background(), pod)
	Expect(err).ToNot(HaveOccurred())
	compareLeases(generateExpectedLease(pod, "Pod"), actualLeaseFromPod)
}
