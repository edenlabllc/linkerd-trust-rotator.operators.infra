/*
Copyright 2025 Edenlab.

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

package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	_ "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	trv1alpha1 "linkerd-trust-rotator.operators.infra/api/v1alpha1"
	"linkerd-trust-rotator.operators.infra/internal/config_map"
	"linkerd-trust-rotator.operators.infra/internal/rollout"
	"linkerd-trust-rotator.operators.infra/internal/secret"
	"linkerd-trust-rotator.operators.infra/internal/status"
)

const (
	frequency                   = time.Second * 10
	linkerdIdentityIssuerSecret = "linkerd-identity-issuer"
)

// LinkerdTrustRotationReconciler reconciles a LinkerdTrustRotation object
type LinkerdTrustRotationReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=trust-anchor.linkerd.edenlab.io,resources=linkerdtrustrotations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=trust-anchor.linkerd.edenlab.io,resources=linkerdtrustrotations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=trust-anchor.linkerd.edenlab.io,resources=linkerdtrustrotations/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the LinkerdTrustRotation object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.1/pkg/reconcile
func (r *LinkerdTrustRotationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var bundleStatus trv1alpha1.BundleState

	reqLogger := logf.FromContext(ctx)

	statusMgr := status.New(r.Client, r.Scheme, reqLogger)
	configMapMgr := config_map.New(r.Client, r.Scheme, reqLogger)
	secretMgr := secret.New(r.Client, r.Scheme, reqLogger)
	rolloutMgr := rollout.New(r.Client, r.Scheme, reqLogger, statusMgr)
	lTR := &trv1alpha1.LinkerdTrustRotation{}

	if err := r.Client.Get(ctx, req.NamespacedName, lTR); err != nil {
		if apierrors.IsNotFound(err) {
			reqLogger.Error(nil, fmt.Sprintf("Can not find CRD by name: %s", req.Name))
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, err
	}

	if err := statusMgr.SetPhase(ctx, lTR,
		status.PhasePtr(trv1alpha1.PhaseIdle),
		status.ReasonPtr(""),
		status.StringPtr("Starting to watch for changes to the Linkerd trust anchor certificate"),
	); err != nil {
		return ctrl.Result{}, err
	}

	if err := statusMgr.SetProgress(ctx, lTR, true, nil, nil); err != nil {
		return ctrl.Result{}, err
	}

	switch {
	case lTR.Spec.Trigger.OnTrustAnchorSecretsDiff && !lTR.Spec.Trigger.OnTrustRootsConfigMapChange:
		secretResult, err := secretMgr.EnsureTrustSecrets(ctx, lTR)
		if err != nil {
			return ctrl.Result{}, err
		}

		bundleStatus = trv1alpha1.BundleStateSingle
		if secretResult.Diverged {
			bundleStatus = trv1alpha1.BundleStateOverlap
		}

		if err := statusMgr.SetTrustInfo(ctx, lTR, status.BundlePtr(bundleStatus), secretResult.CurrentFP, secretResult.PreviousFP); err != nil {
			return ctrl.Result{}, err
		}
	case lTR.Spec.Trigger.OnTrustRootsConfigMapChange && !lTR.Spec.Trigger.OnTrustAnchorSecretsDiff:
		configMapResult, err := configMapMgr.LoadAndInspectCMBundle(ctx, lTR)
		if err != nil {
			return ctrl.Result{}, err
		}

		bundleStatus = trv1alpha1.BundleStateSingle
		if configMapResult.State == trv1alpha1.BundleStateOverlap {
			bundleStatus = trv1alpha1.BundleStateOverlap
		}

		var currentFP, previousFP string
		switch len(configMapResult.Fps) {
		case 1:
			currentFP = fmt.Sprintf("sha256:%s", configMapResult.Fps[0])
			previousFP = fmt.Sprintf("sha256:%s", configMapResult.Fps[0])
		case 2:
			currentFP = fmt.Sprintf("sha256:%s", configMapResult.Fps[1])
			previousFP = fmt.Sprintf("sha256:%s", configMapResult.Fps[0])
		default:
			return ctrl.Result{}, fmt.Errorf("more than 2 or 0 fingerprints found in config map")
		}

		if err := statusMgr.SetTrustInfo(ctx, lTR, status.BundlePtr(bundleStatus), currentFP, previousFP); err != nil {
			return ctrl.Result{}, err
		}
	case lTR.Spec.Trigger.OnTrustAnchorSecretsDiff && lTR.Spec.Trigger.OnTrustRootsConfigMapChange:
		secretResult, err := secretMgr.EnsureTrustSecrets(ctx, lTR)
		if err != nil {
			return ctrl.Result{}, err
		}

		configMapResult, err := configMapMgr.LoadAndInspectCMBundle(ctx, lTR)
		if err != nil {
			return ctrl.Result{}, err
		}

		bundleStatus = trv1alpha1.BundleStateSingle
		if secretResult.Diverged && configMapResult.State == trv1alpha1.BundleStateOverlap {
			bundleStatus = trv1alpha1.BundleStateOverlap
		}

		if err := statusMgr.SetTrustInfo(ctx, lTR, status.BundlePtr(bundleStatus), secretResult.CurrentFP, secretResult.PreviousFP); err != nil {
			return ctrl.Result{}, err
		}
	default:
		return ctrl.Result{},
			fmt.Errorf("no rotation trigger enabled: at least one of trigger.onConfigMapChange or trigger.requireSecretsDivergence must be true")
	}

	if bundleStatus == trv1alpha1.BundleStateOverlap {
		if lTR.Spec.DryRun {
			var workItemDryRun []rollout.WorkItemDryRun

			plane, err := rolloutMgr.SelectLinkerdDataPlane(ctx, lTR)
			if err != nil {
				return ctrl.Result{}, err
			}

			for _, item := range plane.Queue {
				workItemDryRun = append(workItemDryRun, *item.WorkItemDryRun)
			}

			dryRun, err := yaml.Marshal(workItemDryRun)
			if err != nil {
				return ctrl.Result{}, err
			}

			if err := statusMgr.SetDryRunOutput(ctx, lTR, string(dryRun)); err != nil {
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, nil
		}

		if lTR.Status.Retries != nil && lTR.Status.Retries.Count > lTR.Spec.Protection.MaxRolloutFailures {
			msg := fmt.Sprintf("max retry limit reached (%d > %d); stopping rollout",
				lTR.Status.Retries.Count, lTR.Spec.Protection.MaxRolloutFailures)

			reqLogger.Info(msg)
			r.Recorder.Event(lTR, corev1.EventTypeWarning, "MaxRetriesExceeded", msg)
			if err := statusMgr.SetPhase(ctx, lTR,
				status.PhasePtr(trv1alpha1.PhaseFailed),
				status.ReasonPtr(trv1alpha1.ReasonMaxRetriesExceeded),
				status.StringPtr(msg)); err != nil {
				return ctrl.Result{}, err
			}

			return ctrl.Result{RequeueAfter: time.Minute * 1}, nil
		}

		if err := statusMgr.SetPhase(ctx, lTR,
			status.PhasePtr(trv1alpha1.PhaseDetecting),
			status.ReasonPtr(trv1alpha1.ReasonSecretsDiverged),
			status.StringPtr(fmt.Sprintf("Certificate mismatch detected for Linkerd trust anchor: %s vs %s",
				lTR.Spec.Linkerd.TrustAnchorSecret, lTR.Spec.Linkerd.PreviousTrustAnchorSecret,
			)),
		); err != nil {
			return ctrl.Result{}, err
		}

		if err := waitWithPurpose(ctx, reqLogger, lTR.Spec.Protection.BeforeRolloutDelay, "before rollout delay"); err != nil {
			return ctrl.Result{}, err
		}

		if err := secretMgr.DeleteSecrets(ctx, lTR, linkerdIdentityIssuerSecret); err != nil {
			return ctrl.Result{}, err
		}

		if err := rolloutMgr.RestartLinkerdControlPlane(ctx, lTR); err != nil {
			if err := statusMgr.MarkFailed(ctx, lTR, trv1alpha1.ReasonRotationFailed,
				err.Error()); err != nil {
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, err
		}

		if err := rolloutMgr.RestartLinkerdDataPlane(ctx, lTR); err != nil {
			if err := statusMgr.MarkFailed(ctx, lTR, trv1alpha1.ReasonRotationFailed,
				err.Error()); err != nil {
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, err
		}

		if err := secretMgr.DeleteSecrets(ctx, lTR, lTR.Spec.Linkerd.PreviousTrustAnchorSecret); err != nil {
			return ctrl.Result{}, err
		}

		if lTR.Spec.Protection.RetriggerRolloutAfterCleanup {
			if err := waitWithPurpose(ctx, reqLogger, lTR.Spec.Protection.HoldAfterCleanup, "hold after cleanup"); err != nil {
				return ctrl.Result{}, err
			}

			if _, err := secretMgr.EnsureTrustSecrets(ctx, lTR); err != nil {
				return ctrl.Result{}, err
			}

			if err := rolloutMgr.RestartLinkerdDataPlane(ctx, lTR); err != nil {
				if err := statusMgr.MarkFailed(ctx, lTR, trv1alpha1.ReasonRotationFailed,
					err.Error()); err != nil {
					return ctrl.Result{}, err
				}

				return ctrl.Result{}, err
			}
		}

		if err := statusMgr.MarkSucceeded(ctx, lTR, "Linkerd trust anchor certificate rotation completed successfully"); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{RequeueAfter: frequency}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *LinkerdTrustRotationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorderFor("linkerdtrustrotation")
	return ctrl.NewControllerManagedBy(mgr).
		For(&trv1alpha1.LinkerdTrustRotation{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Named("linkerdtrustrotation").
		Complete(r)
}

// waitWithPurpose waits for the given duration (if > 0) while respecting context cancellation.
// `purpose` is a short label used in logs, e.g. "pre-rollout delay" or "hold-before-cleanup".
func waitWithPurpose(ctx context.Context, logger logr.Logger, d *metav1.Duration, purpose string) error {
	if d == nil || d.Duration <= 0 {
		return nil
	}

	logger.Info(fmt.Sprintf("Waiting, purpose %s, duration %s", purpose, d.Duration.String()))

	timer := time.NewTimer(d.Duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		logger.Info(fmt.Sprintf("Wait cancelled by context, purpose %s, err: %v", purpose, ctx.Err()))
		return ctx.Err()
	case <-timer.C:
		logger.Info(fmt.Sprintf("Wait finished, purpose %s", purpose))
		return nil
	}
}
