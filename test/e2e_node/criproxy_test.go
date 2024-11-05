//go:build linux
// +build linux

/*
Copyright 2024 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2enode

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	kubeletevents "k8s.io/kubernetes/pkg/kubelet/events"
	"k8s.io/kubernetes/pkg/kubelet/images"
	"k8s.io/kubernetes/test/e2e/feature"
	"k8s.io/kubernetes/test/e2e/framework"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
	"k8s.io/kubernetes/test/e2e_node/criproxy"
	imageutils "k8s.io/kubernetes/test/utils/image"
	admissionapi "k8s.io/pod-security-admission/api"
)

// Examples of using CRI proxy
var _ = SIGDescribe(feature.CriProxy, framework.WithSerial(), func() {
	f := framework.NewDefaultFramework("cri-proxy-example")
	f.NamespacePodSecurityLevel = admissionapi.LevelPrivileged

	ginkgo.Context("Inject a pull image error exception into the CriProxy", func() {
		ginkgo.BeforeEach(func() {
			if err := resetCRIProxyInjector(e2eCriProxy); err != nil {
				ginkgo.Skip("Skip the test since the CRI Proxy is undefined.")
			}
		})

		ginkgo.AfterEach(func() {
			err := resetCRIProxyInjector(e2eCriProxy)
			framework.ExpectNoError(err)
		})

		ginkgo.It("Pod failed to start due to an image pull error.", func(ctx context.Context) {
			expectedErr := fmt.Errorf("PullImage failed")
			err := addCRIProxyInjector(e2eCriProxy, func(apiName string) error {
				if apiName == criproxy.PullImage {
					return expectedErr
				}
				return nil
			})
			framework.ExpectNoError(err)

			pod := e2epod.NewPodClient(f).Create(ctx, newPullImageAlwaysPod())
			podErr := e2epod.WaitForPodCondition(ctx, f.ClientSet, f.Namespace.Name, pod.Name, "ImagePullBackOff", 1*time.Minute, func(pod *v1.Pod) (bool, error) {
				if len(pod.Status.ContainerStatuses) > 0 && pod.Status.Reason == images.ErrImagePullBackOff.Error() {
					return true, nil
				}
				return false, nil
			})
			gomega.Expect(podErr).To(gomega.HaveOccurred())

			eventMsg, err := getFailedToPullImageMsg(ctx, f, pod.Name)
			framework.ExpectNoError(err)
			isExpectedErrMsg := strings.Contains(eventMsg, expectedErr.Error())
			gomega.Expect(isExpectedErrMsg).To(gomega.BeTrueBecause("we injected an exception into the PullImage interface of the cri proxy"))
		})
	})

	ginkgo.Context("Inject a pull image error exception, then reset it, into the CriProxy", func() {
		ginkgo.BeforeEach(func() {
			if err := resetCRIProxyInjector(); err != nil {
				ginkgo.Skip("Skip the test since the CRI Proxy is undefined.")
			}
		})

		ginkgo.AfterEach(func() {
			err := resetCRIProxyInjector()
			framework.ExpectNoError(err)
		})

		ginkgo.It("Image pull retry backs off on error.", func(ctx context.Context) {
			// inject PullImage failed to trigger backoff
			expectedErr := fmt.Errorf("PullImage failed")
			err := addCRIProxyInjector(func(apiName string) error {
				if apiName == criproxy.PullImage {
					return expectedErr
				}
				return nil
			})
			framework.ExpectNoError(err)

			pod := e2epod.NewPodClient(f).Create(ctx, newPullImageAlwaysPod())
			podErr := e2epod.WaitForPodCondition(ctx, f.ClientSet, f.Namespace.Name, pod.Name, "ImagePullBackOff", 1*time.Minute, func(pod *v1.Pod) (bool, error) {
				if len(pod.Status.ContainerStatuses) > 0 && pod.Status.Reason == images.ErrImagePullBackOff.Error() {
					return true, nil
				}
				return false, nil
			})
			gomega.Expect(podErr).To(gomega.HaveOccurred())

			eventMsg, err := getFailedToPullImageMsg(ctx, f, pod.Name)
			framework.ExpectNoError(err)
			isExpectedErrMsg := strings.Contains(eventMsg, expectedErr.Error())
			gomega.Expect(isExpectedErrMsg).To(gomega.BeTrueBecause("we injected an exception into the PullImage interface of the cri proxy"))

			// Wait for ~40s worth of backoffs to occur so we can confirm the backoff growth.
			podErr = e2epod.WaitForPodContainerStarted(ctx, f.ClientSet, f.Namespace.Name, pod.Name, 0, 40*time.Second)
			gomega.Expect(podErr).To(gomega.HaveOccurred(), "Expected container not to start from repeadedly backing off image pulls")

			// Remove error so after next backoff we will succeed.
			_ = resetCRIProxyInjector()

			podErr = e2epod.WaitForPodRunningInNamespace(ctx, f.ClientSet, pod)
			framework.ExpectNoError(podErr)

			// Parse observed backoffs (TODO: don't use Events, but logs) and compare to expectations
			durations, err := getImageBackOffDurations(ctx, f, pod.Name)
			framework.ExpectNoError(err)
			gomega.Expect(durations).Error().ShouldNot(gomega.BeNil(), "Should have observed backoffs in Pod event log")
			gomega.Expect(durations[0]).Should(gomega.BeNumerically("~", time.Duration(10*time.Second), time.Duration(2*time.Second)))
			// TODO: and check the next set of durations are 2x, etc

		})
	})

	ginkgo.Context("Inject a pull image timeout exception into the CriProxy", func() {
		ginkgo.BeforeEach(func() {
			if err := resetCRIProxyInjector(e2eCriProxy); err != nil {
				ginkgo.Skip("Skip the test since the CRI Proxy is undefined.")
			}
		})

		ginkgo.AfterEach(func() {
			err := resetCRIProxyInjector(e2eCriProxy)
			framework.ExpectNoError(err)
		})

		ginkgo.It("Image pull time exceeded 10 seconds", func(ctx context.Context) {
			const delayTime = 10 * time.Second
			err := addCRIProxyInjector(e2eCriProxy, func(apiName string) error {
				if apiName == criproxy.PullImage {
					time.Sleep(10 * time.Second)
				}
				return nil
			})
			framework.ExpectNoError(err)

			pod := e2epod.NewPodClient(f).Create(ctx, newPullImageAlwaysPod())
			podErr := e2epod.WaitForPodRunningInNamespace(ctx, f.ClientSet, pod)
			framework.ExpectNoError(podErr)

			imagePullDuration, err := getPodImagePullDuration(ctx, f, pod.Name)
			framework.ExpectNoError(err)

			gomega.Expect(imagePullDuration).To(gomega.BeNumerically(">=", delayTime), "PullImages should take more than 10 seconds")
		})
	})
})

func getFailedToPullImageMsg(ctx context.Context, f *framework.Framework, podName string) (string, error) {
	events, err := f.ClientSet.CoreV1().Events(f.Namespace.Name).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}

	for _, event := range events.Items {
		if event.Reason == kubeletevents.FailedToPullImage && event.InvolvedObject.Name == podName {
			return event.Message, nil
		}
	}

	return "", fmt.Errorf("failed to find FailedToPullImage event for pod: %s", podName)
}

func getImageBackOffDurations(ctx context.Context, f *framework.Framework, podName string) ([]time.Duration, error) {
	events, err := f.ClientSet.CoreV1().Events(f.Namespace.Name).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var backoffs []time.Duration

	type BackOffRecord struct {
		initialEventTime  time.Time
		backoffEventTimes []time.Time
		duration          time.Duration
	}

	records := make(map[int]*BackOffRecord)
	records[0] = &BackOffRecord{}
	var backoffCount int
	var pullTime time.Time
	var r *BackOffRecord
	// I'm doing this here for events but really it needs to be off kubelet logs or some kind of synchronous watch
	// Because the events normalize to the latest
	// But this is the idea
	for _, event := range events.Items {
		if event.InvolvedObject.Name == podName {
			switch event.Reason {
			case kubeletevents.PullingImage:
				if !pullTime.IsZero() {
					if event.FirstTimestamp.Time.After(pullTime) {
						r = records[backoffCount]
						r.duration = r.initialEventTime.Sub(r.backoffEventTimes[len(r.backoffEventTimes)-1])
						backoffs = append(backoffs, r.duration)
						backoffCount++
					}
				}
				pullTime = event.FirstTimestamp.Time
				records[backoffCount].initialEventTime = pullTime
			case kubeletevents.BackOffPullImage:
				current := records[backoffCount].backoffEventTimes
				current = append(current, event.FirstTimestamp.Time)
			}
		}
	}
	return backoffs, nil
}

func getPodImagePullDuration(ctx context.Context, f *framework.Framework, podName string) (time.Duration, error) {
	events, err := f.ClientSet.CoreV1().Events(f.Namespace.Name).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, err
	}

	var startTime, endTime time.Time
	for _, event := range events.Items {
		if event.InvolvedObject.Name == podName {
			switch event.Reason {
			case kubeletevents.PullingImage:
				startTime = event.FirstTimestamp.Time
			case kubeletevents.PulledImage:
				endTime = event.FirstTimestamp.Time
			}
		}
	}

	if startTime.IsZero() || endTime.IsZero() {
		return 0, fmt.Errorf("failed to find both PullingImage and PulledImage events for pod: %s", podName)
	}

	return endTime.Sub(startTime), nil
}

func newPullImageAlwaysPod() *v1.Pod {
	podName := "cri-proxy-test-" + string(uuid.NewUUID())
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Image:           imageutils.GetPauseImageName(),
					Name:            podName,
					ImagePullPolicy: v1.PullAlways,
				},
			},
		},
	}
	return pod
}
