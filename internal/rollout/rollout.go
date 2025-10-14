package rollout

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/go-logr/logr"
	v1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"linkerd-trust-rotator.operators.infra/internal/status"
)

const (
	restartedAtKey      = "kubectl.kubernetes.io/restartedAt"
	rolloutPollInterval = 2 * time.Second
	rolloutPerLimit     = 5 * time.Minute
)

type ManageRollout struct {
	Client client.Client
	Scheme *runtime.Scheme
	Logger logr.Logger
	Status *status.ManageStatus
}

// New returns a new secret manager.
func New(c client.Client, s *runtime.Scheme, l logr.Logger, status *status.ManageStatus) *ManageRollout {
	return &ManageRollout{Client: c, Scheme: s, Logger: l.WithName("Rollout"), Status: status}
}

// BumpRestartAnnotation bumps an annotation to trigger restart/rolling.
// For typed workloads (Deploy/STS/DS) it updates pod template.
// For CRDs it tries (in order): special Strimzi case -> .spec.template -> .spec.pods[] -> resource metadata.
func (m *ManageRollout) bumpRestartAnnotation(ctx context.Context, obj client.Object) error {
	return m.bumpAnnotationGeneric(ctx, obj, restartedAtKey, time.Now().UTC().Format(time.RFC3339))
}

// bumpRestartAnnotation patches pod template annotation with current timestamp,
// triggering a new rollout (the same as `kubectl rollout restart`).
func (m *ManageRollout) bumpAnnotationGeneric(ctx context.Context, obj client.Object, key, value string) error {
	switch o := obj.(type) {
	case *v1.Deployment:
		orig := o.DeepCopy()
		if o.Spec.Template.Annotations == nil {
			o.Spec.Template.Annotations = map[string]string{}
		}
		o.Spec.Template.Annotations[key] = value
		return m.Client.Patch(ctx, o, client.MergeFrom(orig))

	case *v1.StatefulSet:
		orig := o.DeepCopy()
		if o.Spec.Template.Annotations == nil {
			o.Spec.Template.Annotations = map[string]string{}
		}
		o.Spec.Template.Annotations[key] = value
		return m.Client.Patch(ctx, o, client.MergeFrom(orig))

	case *v1.DaemonSet:
		orig := o.DeepCopy()
		if o.Spec.Template.Annotations == nil {
			o.Spec.Template.Annotations = map[string]string{}
		}
		o.Spec.Template.Annotations[key] = value
		return m.Client.Patch(ctx, o, client.MergeFrom(orig))
	case *unstructured.Unstructured:
		return m.bumpAnnotationUnstructured(ctx, o, key, value)

	default:
		return fmt.Errorf("unsupported type for annotation bump: %T", obj)
	}
}

func (m *ManageRollout) bumpAnnotationUnstructured(ctx context.Context, u *unstructured.Unstructured, key, value string) error {
	// Fallback: set on resource metadata (works for CRDs with operator-defined triggers)
	orig := u.DeepCopy()
	ann := u.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}

	ann[key] = value
	u.SetAnnotations(ann)
	return m.Client.Patch(ctx, u, client.MergeFrom(orig))
}

// restartStatefulSetByDelete performs a manual rolling restart by deleting pods one-by-one.
// Order: highest ordinal -> lowest (N-1 ... 0). Waits for each pod to become Ready again.
func (m *ManageRollout) restartStatefulSetByDelete(ctx context.Context, sts *v1.StatefulSet, perPodTimeout time.Duration) error {
	// List pods by StatefulSet selector
	selector, err := metav1.LabelSelectorAsSelector(sts.Spec.Selector)
	if err != nil {
		return fmt.Errorf("build selector for StatefulSet %s/%s: %w", sts.Namespace, sts.Name, err)
	}

	var pods corev1.PodList
	if err := m.Client.List(ctx, &pods,
		client.InNamespace(sts.Namespace),
		client.MatchingLabelsSelector{Selector: selector},
	); err != nil {
		return fmt.Errorf("list pods for StatefulSet %s/%s: %w", sts.Namespace, sts.Name, err)
	}

	if len(pods.Items) == 0 {
		m.Logger.Info(fmt.Sprintf("No pods found for StatefulSet %s/%s (nothing to delete)", sts.Namespace, sts.Name))
		return nil
	}

	// Sort pods by descending ordinal: <name>-N-1 ... <name>-0
	sort.Slice(pods.Items, func(i, j int) bool {
		return podOrdinal(pods.Items[i].Name) > podOrdinal(pods.Items[j].Name)
	})

	for i := range pods.Items {
		p := pods.Items[i] // copy
		if err := m.deletePodAndWaitSameNameReady(ctx, &p, perPodTimeout); err != nil {
			return fmt.Errorf("rolloutDelete %s/%s pod %s: %w", p.Namespace, sts.Name, p.Name, err)
		}
	}

	return nil
}

// deletePodAndWaitSameNameReady deletes the given Pod and waits until a Pod with the same name
// appears Running and Ready again (which is how StatefulSet recreates its pods).
func (m *ManageRollout) deletePodAndWaitSameNameReady(ctx context.Context, p *corev1.Pod, timeout time.Duration) error {
	// Delete with default grace; adjust if you want immediate termination
	if err := m.Client.Delete(ctx, p, &client.DeleteOptions{
		PropagationPolicy:  func() *metav1.DeletionPropagation { bg := metav1.DeletePropagationBackground; return &bg }(),
		GracePeriodSeconds: nil, // use Pod's own terminationGracePeriodSeconds
	}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete pod %s/%s: %w", p.Namespace, p.Name, err)
	}

	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()

	key := types.NamespacedName{Namespace: p.Namespace, Name: p.Name}

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}

		var cur corev1.Pod
		err := m.Client.Get(ctx, key, &cur)
		if apierrors.IsNotFound(err) {
			// Still recreating — keep polling
			continue
		}
		if err != nil {
			return err
		}

		// Ready when Running + PodReady=True and not terminating
		if cur.DeletionTimestamp == nil &&
			cur.Status.Phase == corev1.PodRunning &&
			podReady(&cur) {
			return nil
		}
	}

	return fmt.Errorf("timeout waiting pod %s/%s to be Ready after delete", p.Namespace, p.Name)
}

// waitCRByAnnotationAndStatus is a generic waiter for any CRD.
// It polls the object by GVK and key, and returns when:
//   - statusOK(u) == true
//   - and if requireAnnoCleared == true: metadata.annotations[annoKey] is absent or empty
//
// Use this for CRDs that either:
//   - have a known readiness predicate (statusOK), and
//   - optionally clear a "manual rolling" annotation after finishing.
func (m *ManageRollout) waitCRByAnnotationAndStatus(
	ctx context.Context,
	key types.NamespacedName,
	u *unstructured.Unstructured,
	annoKey string,
	requireAnnoCleared bool,
	timeout time.Duration,
) error {
	ticker := time.NewTicker(rolloutPollInterval)
	defer ticker.Stop()

	deadline := time.Now().Add(timeout)
	statusOK := func(u *unstructured.Unstructured) bool {
		ready, _, _ := unstructured.NestedInt64(u.Object, "status", "readyPods")
		total, _, _ := unstructured.NestedInt64(u.Object, "status", "pods")
		obs, _, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
		return total > 0 && ready == total && obs >= u.GetGeneration()
	}

	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for %s", u.GroupVersionKind().String())
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		cur := &unstructured.Unstructured{}
		cur.SetGroupVersionKind(u.GroupVersionKind())
		if err := m.Client.Get(ctx, key, cur); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}

			return err
		}

		ok := statusOK(cur)
		if requireAnnoCleared {
			ann := cur.GetAnnotations()
			cleared := ann == nil || ann[annoKey] == ""
			if ok && cleared {
				return nil
			}
		} else {
			if ok {
				return nil
			}
		}
	}
}

// waitDeploymentRolledOut waits until Deployment is fully rolled out,
// following the same logic as `kubectl rollout status`.
func (m *ManageRollout) waitDeploymentRolledOut(ctx context.Context, key types.NamespacedName, timeout time.Duration) error {
	ticker := time.NewTicker(rolloutPollInterval)
	defer ticker.Stop()

	deadline := time.Now().Add(timeout)

	for {
		// timeout check
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for Deployment rollout")
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		var cur v1.Deployment
		if err := m.Client.Get(ctx, key, &cur); err != nil {
			if apierrors.IsNotFound(err) {
				// unlikely for Deployment, but retry
				continue
			}
			return err
		}

		// replicas defaults to 1 if not set
		var replicas int32 = 1
		if cur.Spec.Replicas != nil {
			replicas = *cur.Spec.Replicas
		}

		ready := cur.Status.UpdatedReplicas == replicas &&
			cur.Status.ReadyReplicas == replicas &&
			cur.Status.UnavailableReplicas == 0 &&
			cur.Status.ObservedGeneration >= cur.Generation

		if ready {
			return nil
		}
	}
}

// waitStatefulSetRolledOut waits until StatefulSet has finished rolling update.
func (m *ManageRollout) waitStatefulSetRolledOut(ctx context.Context, key types.NamespacedName, timeout time.Duration) error {
	ticker := time.NewTicker(rolloutPollInterval)
	defer ticker.Stop()

	deadline := time.Now().Add(timeout)

	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for StatefulSet rollout")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		var cur v1.StatefulSet
		if err := m.Client.Get(ctx, key, &cur); err != nil {
			if apierrors.IsNotFound(err) {
				// Unlikely for StatefulSet; retry next tick.
				continue
			}
			return err
		}

		// default replicas = 1 if not set
		var replicas int32 = 1
		if cur.Spec.Replicas != nil {
			replicas = *cur.Spec.Replicas
		}

		ready := cur.Status.ReadyReplicas == replicas &&
			cur.Status.CurrentRevision == cur.Status.UpdateRevision &&
			cur.Status.ObservedGeneration >= cur.Generation

		if ready {
			return nil
		}
	}
}

// waitDaemonSetRolledOut waits until DaemonSet has finished rolling update.
// Note: with OnDelete strategy, bumping the template won't roll pods; we fail early.
func (m *ManageRollout) waitDaemonSetRolledOut(ctx context.Context, key types.NamespacedName, timeout time.Duration) error {
	ticker := time.NewTicker(rolloutPollInterval)
	defer ticker.Stop()

	deadline := time.Now().Add(timeout)

	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for Daemonset rollout")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		var cur v1.DaemonSet
		if err := m.Client.Get(ctx, key, &cur); err != nil {
			if apierrors.IsNotFound(err) {
				// Unlikely for DaemonSet; retry next tick.
				continue
			}
			return err
		}

		if cur.Spec.UpdateStrategy.Type == v1.OnDeleteDaemonSetStrategyType {
			return fmt.Errorf("Daemonset %s uses OnDelete strategy: template bump won't roll pods", key.String())
		}

		desired := cur.Status.DesiredNumberScheduled
		ready := cur.Status.UpdatedNumberScheduled == desired &&
			cur.Status.NumberAvailable == desired &&
			cur.Status.NumberMisscheduled == 0 &&
			cur.Status.ObservedGeneration >= cur.Generation

		if ready {
			return nil
		}
	}
}

func (m *ManageRollout) waitJobSucceeded(ctx context.Context, ns, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(rolloutPollInterval)
	defer tick.Stop()

	key := client.ObjectKey{Namespace: ns, Name: name}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}

		var cur batchv1.Job
		if err := m.Client.Get(ctx, key, &cur); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}

			return err
		}

		for _, c := range cur.Status.Conditions {
			if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
				return fmt.Errorf("linkerd check job failed: %s", c.Message)
			}

			if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
				return nil
			}
		}
	}

	return fmt.Errorf("timeout waiting for linkerd check job %s/%s", ns, name)
}
