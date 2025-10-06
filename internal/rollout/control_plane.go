package rollout

import (
	"context"
	"fmt"
	v1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	trv1alpha1 "linkerd-trust-rotator.operators.infra/api/v1alpha1"
	"linkerd-trust-rotator.operators.infra/internal/status"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sort"
)

const (
	LabelCPNamespace = "linkerd.io/control-plane-ns"
)

func (m *ManageRollout) SelectLinkerdControlPlane(ctx context.Context, obj *trv1alpha1.LinkerdTrustRotation) (*v1.DeploymentList, error) {
	reqs := labels.NewSelector()
	cpList := &v1.DeploymentList{}
	namespace := obj.Spec.Namespace

	if r, err := labels.NewRequirement(LabelCPNamespace, selection.In, []string{namespace}); err == nil {
		reqs = reqs.Add(*r)
	} else {
		return nil, fmt.Errorf("build selector: %w", err)
	}

	if err := m.Client.List(ctx, cpList, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: reqs}); err != nil {
		return nil, err
	}

	return cpList, nil
}

// RestartLinkerdControlPlane bumps pod-template annotation for each CP deployment
// and waits until rollout is completed.
func (m *ManageRollout) RestartLinkerdControlPlane(ctx context.Context, obj *trv1alpha1.LinkerdTrustRotation) error {
	deployments, err := m.SelectLinkerdControlPlane(ctx, obj)
	if err != nil {
		return err
	}

	if err := m.Status.SetPhase(ctx, obj,
		status.PhasePtr(trv1alpha1.PhaseRollingCP),
		status.ReasonPtr(trv1alpha1.ReasonCPRestarting),
		status.StringPtr("Starting rollout restart Linkerd control plane"),
	); err != nil {
		return err
	}

	if err := m.Status.SetProgress(ctx, obj, false, nil, nil); err != nil {
		return err
	}

	sort.Slice(deployments.Items, func(i, j int) bool {
		return deployments.Items[i].Name > deployments.Items[j].Name
	})

	for _, dp := range deployments.Items {
		m.Logger.Info(fmt.Sprintf("Start linkerd control plane Deployment: %s/%s restarting", dp.Namespace, dp.Name))
		if err := m.bumpRestartAnnotation(ctx, &dp); err != nil {
			return err
		}

		dsNamespacedName := types.NamespacedName{Namespace: dp.Namespace, Name: dp.Name}
		if err := m.waitDeploymentRolledOut(ctx, dsNamespacedName, rolloutPerLimit); err != nil {
			return err
		}

		m.Logger.Info(fmt.Sprintf("Restarted linkerd control plane Deployment: %s/%s", dp.Namespace, dp.Name))
	}

	if err := m.Status.SetProgress(ctx, obj, true, nil, nil); err != nil {
		return err
	}

	if err := m.Status.SetPhase(ctx, obj,
		status.PhasePtr(trv1alpha1.PhaseRollingCP),
		status.ReasonPtr(trv1alpha1.ReasonCPReady),
		status.StringPtr("Finished restarted Linkerd control plane"),
	); err != nil {
		return err
	}

	return nil
}
