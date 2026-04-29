package taints

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
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
			Expect(taint.TimeAdded).NotTo(BeNil())
		})

		It("should set TimeAdded to current time", func() {
			before := metav1.Now()
			taint := CreateOutOfServiceTaint()
			after := metav1.Now()
			Expect(taint.TimeAdded.Time).To(BeTemporally(">=", before.Time))
			Expect(taint.TimeAdded.Time).To(BeTemporally("<=", after.Time))
		})
	})

	Context("setOutOfTaintFlags", func() {
		BeforeEach(func() {
			taintInfo = OutOfServiceTaintInfo{}
		})

		When("version is supported but not GA", func() {
			DescribeTable("should set Supported=true, GA=false",
				func(major, minor string) {
					err := setOutOfTaintFlags(&version.Info{Major: major, Minor: minor})
					Expect(err).NotTo(HaveOccurred())
					Expect(taintInfo.Supported).To(BeTrue())
					Expect(taintInfo.GA).To(BeFalse())
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
					Expect(taintInfo.Supported).To(BeTrue())
					Expect(taintInfo.GA).To(BeTrue())
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
					Expect(taintInfo.Supported).To(BeFalse())
					Expect(taintInfo.GA).To(BeFalse())
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
					Expect(taintInfo.Supported).To(BeFalse())
					Expect(taintInfo.GA).To(BeFalse())
				},
				Entry("invalid minor version", "1", "%24"),
				Entry("invalid major version", "1+", "26"),
			)
		})
	})

	Context("GetOutOfServiceTaintInfo", func() {
		It("should return the taint info", func() {
			taintInfo = OutOfServiceTaintInfo{Supported: true, GA: false}
			info := GetOutOfServiceTaintInfo()
			Expect(info.Supported).To(BeTrue())
			Expect(info.GA).To(BeFalse())
		})
	})
})
