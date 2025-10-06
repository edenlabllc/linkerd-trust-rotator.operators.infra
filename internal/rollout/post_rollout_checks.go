package rollout

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sigs.k8s.io/controller-runtime/pkg/client"

	//v1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"time"
)

const (
	// DefaultLinkerdCLIImage can be overridden via CR.
	defaultLinkerdCLIImage = "ghcr.io/linkerd/cli-bin:stable-2.14.10"
	jobNamePrefix          = "linkerd-proxy-check"
	jobSA                  = "l5d-check"
)

type CheckProxyOptions struct {
	CLIImage      string
	TargetNs      string
	JobNs         string
	JobNameSuffix string
	Timeout       time.Duration
}

func NewCheckProxyOptions(image, targetNs, jobNS, jobNameSuffix string, timeout time.Duration) *CheckProxyOptions {
	return &CheckProxyOptions{
		CLIImage:      image,
		TargetNs:      targetNs,
		JobNs:         jobNS,
		JobNameSuffix: jobNameSuffix,
		Timeout:       timeout,
	}
}

func (m *ManageRollout) runLinkerdCheckJob(ctx context.Context, options *CheckProxyOptions) error {
	cliImage := options.CLIImage
	if len(cliImage) == 0 {
		cliImage = defaultLinkerdCLIImage
	}

	sum := sha1.Sum([]byte(options.JobNameSuffix))
	jobName := fmt.Sprintf("%s-%s-%s", jobNamePrefix, options.TargetNs, hex.EncodeToString(sum[:])[:7])
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: options.JobNs,
			Labels:    map[string]string{"app": jobNamePrefix},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptrInt32(0),
			TTLSecondsAfterFinished: ptrInt32(60),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("%s-%s", jobNamePrefix, options.TargetNs),
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: jobSA,
					RestartPolicy:      corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:  jobNamePrefix,
							Image: cliImage,
							Args: []string{
								"check",
								"--proxy",
								"--namespace", options.TargetNs,
								"--linkerd-namespace", options.JobNs,
								"--wait=2m", "--verbose"},
							// If cluster needs RBAC or KUBECONFIG, you may mount ServiceAccount token automatically.
						},
					},
				},
			},
		},
	}

	// Create or replace the job
	pp := metav1.DeletePropagationForeground
	_ = m.Client.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &pp}) // best-effort cleanup previous
	if err := m.Client.Create(ctx, job); err != nil {
		return err
	}

	return m.waitJobSucceeded(ctx, job.Namespace, job.Name, options.Timeout)
}
