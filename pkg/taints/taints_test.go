package taints

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("Taint utilities", func() {
	Context("TaintExists", func() {
		taint1 := corev1.Taint{Key: "key1", Effect: corev1.TaintEffectNoSchedule}
		taint2 := corev1.Taint{Key: "key2", Effect: corev1.TaintEffectNoExecute}
		taint3 := corev1.Taint{Key: "key3", Effect: corev1.TaintEffectPreferNoSchedule}

		When("taint exists in list", func() {
			It("should return true", func() {
				taints := []corev1.Taint{taint1, taint2}
				Expect(TaintExists(taints, &taint1)).To(BeTrue())
				Expect(TaintExists(taints, &taint2)).To(BeTrue())
			})
		})

		When("taint does not exist in list", func() {
			It("should return false", func() {
				taints := []corev1.Taint{taint1, taint2}
				Expect(TaintExists(taints, &taint3)).To(BeFalse())
			})
		})

		When("list is empty", func() {
			It("should return false", func() {
				taints := []corev1.Taint{}
				Expect(TaintExists(taints, &taint1)).To(BeFalse())
			})
		})
	})

	Context("FilterOutTaint", func() {
		taint1 := corev1.Taint{Key: "key1", Effect: corev1.TaintEffectNoSchedule, Value: "value1"}
		taint2 := corev1.Taint{Key: "key2", Effect: corev1.TaintEffectNoExecute, Value: "value2"}
		taint3 := corev1.Taint{Key: "key3", Effect: corev1.TaintEffectPreferNoSchedule, Value: "value3"}
		taintToRemove := corev1.Taint{Key: "key2", Effect: corev1.TaintEffectNoExecute}

		When("taint to filter exists", func() {
			It("should return filtered list and true", func() {
				taints := []corev1.Taint{taint1, taint2, taint3}
				newTaints, deleted := FilterOutTaint(taints, &taintToRemove)
				Expect(deleted).To(BeTrue())
				Expect(newTaints).To(HaveLen(2))
				Expect(newTaints).To(ContainElement(taint1))
				Expect(newTaints).To(ContainElement(taint3))
				Expect(newTaints).NotTo(ContainElement(taint2))
			})
		})

		When("taint to filter does not exist", func() {
			It("should return original list and false", func() {
				taints := []corev1.Taint{taint1, taint3}
				newTaints, deleted := FilterOutTaint(taints, &taintToRemove)
				Expect(deleted).To(BeFalse())
				Expect(newTaints).To(HaveLen(2))
				Expect(newTaints).To(ContainElement(taint1))
				Expect(newTaints).To(ContainElement(taint3))
			})
		})

		When("multiple matching taints exist", func() {
			It("should remove all matching taints", func() {
				duplicate := corev1.Taint{Key: "key2", Effect: corev1.TaintEffectNoExecute, Value: "different"}
				taints := []corev1.Taint{taint1, taint2, duplicate, taint3}
				newTaints, deleted := FilterOutTaint(taints, &taintToRemove)
				Expect(deleted).To(BeTrue())
				Expect(newTaints).To(HaveLen(2))
				Expect(newTaints).To(ContainElement(taint1))
				Expect(newTaints).To(ContainElement(taint3))
			})
		})

		When("list is empty", func() {
			It("should return empty list and false", func() {
				taints := []corev1.Taint{}
				newTaints, deleted := FilterOutTaint(taints, &taintToRemove)
				Expect(deleted).To(BeFalse())
				Expect(newTaints).To(BeEmpty())
			})
		})
	})

	Context("CreateOutOfServiceTaint", func() {
		It("should create out-of-service taint with correct fields", func() {
			taint := CreateOutOfServiceTaint()
			Expect(taint.Key).To(Equal(corev1.TaintNodeOutOfService))
			Expect(taint.Value).To(Equal("nodeshutdown"))
			Expect(taint.Effect).To(Equal(corev1.TaintEffectNoExecute))
		})
	})

	Context("setOutOfTaintFlags", func() {
		BeforeEach(func() {
			OutOfServiceInfo = OutOfServiceTaintInfo{}
		})

		When("version is supported but not GA", func() {
			DescribeTable("should set Supported=true, GA=false",
				func(major, minor string) {
					err := setOutOfTaintFlags(&version.Info{Major: major, Minor: minor})
					Expect(err).NotTo(HaveOccurred())
					Expect(OutOfServiceInfo.Supported).To(BeTrue())
					Expect(OutOfServiceInfo.GA).To(BeFalse())
				},
				Entry("version 1.26", "1", "26"),
				Entry("version 1.26+", "1", "26+"),
				Entry("version 1.27", "1", "27"),
				Entry("version 1.26 with trailing chars", "1", "26.5.2#$%+"),
			)
		})

		When("version is GA", func() {
			DescribeTable("should set Supported=true, GA=true",
				func(major, minor string) {
					err := setOutOfTaintFlags(&version.Info{Major: major, Minor: minor})
					Expect(err).NotTo(HaveOccurred())
					Expect(OutOfServiceInfo.Supported).To(BeTrue())
					Expect(OutOfServiceInfo.GA).To(BeTrue())
				},
				Entry("version 1.28", "1", "28"),
				Entry("version 1.28+", "1", "28+"),
				Entry("version 1.29 with trailing chars", "1", "29.5.2#$%+"),
			)
		})

		When("version is not supported", func() {
			DescribeTable("should set Supported=false, GA=false",
				func(major, minor string) {
					err := setOutOfTaintFlags(&version.Info{Major: major, Minor: minor})
					Expect(err).NotTo(HaveOccurred())
					Expect(OutOfServiceInfo.Supported).To(BeFalse())
					Expect(OutOfServiceInfo.GA).To(BeFalse())
				},
				Entry("version 1.24", "1", "24"),
				Entry("version 1.24+", "1", "24+"),
				Entry("version 1.22 with trailing chars", "1", "22.5.2#$%+"),
			)
		})

		When("version format is invalid", func() {
			DescribeTable("should return error",
				func(major, minor string) {
					err := setOutOfTaintFlags(&version.Info{Major: major, Minor: minor})
					Expect(err).To(HaveOccurred())
					Expect(OutOfServiceInfo.Supported).To(BeFalse())
					Expect(OutOfServiceInfo.GA).To(BeFalse())
				},
				Entry("invalid minor version", "1", "%24"),
				Entry("invalid major version", "1+", "26"),
			)
		})
	})
})

var _ = Describe("Node taint operations", func() {
	var k8sClient client.Client
	var testNode *corev1.Node

	BeforeEach(func() {
		// Create a test node
		testNode = &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-node",
			},
		}

		k8sClient = fake.NewClientBuilder().WithObjects(testNode).Build()
	})

	Context("AppendTaintToNode", func() {
		It("should add taint to node and patch it", func() {
			ctx := context.Background()
			taint := corev1.Taint{
				Key:    "test-key",
				Effect: corev1.TaintEffectNoSchedule,
				Value:  "test-value",
			}

			added, err := AppendTaintToNode(ctx, k8sClient, testNode, taint)
			Expect(err).NotTo(HaveOccurred())
			Expect(added).To(BeTrue())

			// Verify taint was added
			updatedNode := &corev1.Node{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testNode), updatedNode)).To(Succeed())
			Expect(TaintExists(updatedNode.Spec.Taints, &taint)).To(BeTrue())

			// Verify TimeAdded was set
			var foundTaint *corev1.Taint
			for i := range updatedNode.Spec.Taints {
				if updatedNode.Spec.Taints[i].MatchTaint(&taint) {
					foundTaint = &updatedNode.Spec.Taints[i]
					break
				}
			}
			Expect(foundTaint).NotTo(BeNil())
			Expect(foundTaint.TimeAdded).NotTo(BeNil())
		})

		It("should return false if taint already exists", func() {
			ctx := context.Background()
			taint := corev1.Taint{
				Key:    "existing-key",
				Effect: corev1.TaintEffectNoExecute,
			}

			// Add taint first time
			added, err := AppendTaintToNode(ctx, k8sClient, testNode, taint)
			Expect(err).NotTo(HaveOccurred())
			Expect(added).To(BeTrue())

			// Get updated node
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testNode), testNode)).To(Succeed())

			// Try to add same taint again
			added, err = AppendTaintToNode(ctx, k8sClient, testNode, taint)
			Expect(err).NotTo(HaveOccurred())
			Expect(added).To(BeFalse())
		})
	})

	Context("RemoveTaintFromNode", func() {
		It("should return false if taint doesn't exist", func() {
			ctx := context.Background()
			taint := corev1.Taint{
				Key:    "nonexistent-key",
				Effect: corev1.TaintEffectNoExecute,
			}

			removed, err := RemoveTaintFromNode(ctx, k8sClient, testNode, taint)
			Expect(err).NotTo(HaveOccurred())
			Expect(removed).To(BeFalse())
		})

		It("should only remove the specified taint and preserve others", func() {
			ctx := context.Background()
			taint1 := corev1.Taint{
				Key:    "taint-to-keep",
				Effect: corev1.TaintEffectNoExecute,
				Value:  "keep-value",
			}
			taint2 := corev1.Taint{
				Key:    "taint-to-remove",
				Effect: corev1.TaintEffectNoSchedule,
				Value:  "remove-value",
			}

			// Add both taints
			added, err := AppendTaintToNode(ctx, k8sClient, testNode, taint1)
			Expect(err).NotTo(HaveOccurred())
			Expect(added).To(BeTrue())

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testNode), testNode)).To(Succeed())

			added, err = AppendTaintToNode(ctx, k8sClient, testNode, taint2)
			Expect(err).NotTo(HaveOccurred())
			Expect(added).To(BeTrue())

			// Get updated node
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testNode), testNode)).To(Succeed())

			// Remove only taint2
			removed, err := RemoveTaintFromNode(ctx, k8sClient, testNode, taint2)
			Expect(err).NotTo(HaveOccurred())
			Expect(removed).To(BeTrue())

			// Verify taint1 still exists but taint2 is gone
			updatedNode := &corev1.Node{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testNode), updatedNode)).To(Succeed())
			Expect(TaintExists(updatedNode.Spec.Taints, &taint1)).To(BeTrue())
			Expect(TaintExists(updatedNode.Spec.Taints, &taint2)).To(BeFalse())
		})
	})
})
