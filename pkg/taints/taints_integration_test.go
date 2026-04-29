package taints

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

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

		It("should preserve existing taints when adding a new taint", func() {
			ctx := context.Background()
			existingTaint := corev1.Taint{
				Key:    "existing-taint",
				Effect: corev1.TaintEffectNoExecute,
				Value:  "existing-value",
			}
			newTaint := corev1.Taint{
				Key:    "new-taint",
				Effect: corev1.TaintEffectNoSchedule,
				Value:  "new-value",
			}

			// Add first taint
			added, err := AppendTaintToNode(ctx, k8sClient, testNode, existingTaint)
			Expect(err).NotTo(HaveOccurred())
			Expect(added).To(BeTrue())

			// Get updated node
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testNode), testNode)).To(Succeed())

			// Add second taint
			added, err = AppendTaintToNode(ctx, k8sClient, testNode, newTaint)
			Expect(err).NotTo(HaveOccurred())
			Expect(added).To(BeTrue())

			// Verify both taints exist
			updatedNode := &corev1.Node{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testNode), updatedNode)).To(Succeed())
			Expect(TaintExists(updatedNode.Spec.Taints, &existingTaint)).To(BeTrue())
			Expect(TaintExists(updatedNode.Spec.Taints, &newTaint)).To(BeTrue())
		})
	})

	Context("RemoveTaintFromNode", func() {
		It("should remove taint from node and patch it", func() {
			ctx := context.Background()
			taint := corev1.Taint{
				Key:    "remove-key",
				Effect: corev1.TaintEffectNoSchedule,
			}

			// Add taint first
			added, err := AppendTaintToNode(ctx, k8sClient, testNode, taint)
			Expect(err).NotTo(HaveOccurred())
			Expect(added).To(BeTrue())

			// Get updated node
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testNode), testNode)).To(Succeed())

			// Remove the taint
			removed, err := RemoveTaintFromNode(ctx, k8sClient, testNode, taint)
			Expect(err).NotTo(HaveOccurred())
			Expect(removed).To(BeTrue())

			// Verify taint was removed
			updatedNode := &corev1.Node{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testNode), updatedNode)).To(Succeed())
			Expect(TaintExists(updatedNode.Spec.Taints, &taint)).To(BeFalse())
		})

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
