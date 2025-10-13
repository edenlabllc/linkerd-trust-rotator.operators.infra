package rollout

import (
	"context"
	"fmt"
	"sort"
	"time"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	trv1alpha1 "linkerd-trust-rotator.operators.infra/api/v1alpha1"
	"linkerd-trust-rotator.operators.infra/internal/status"
)

const (
	Restart = "rolloutRestart" // bump template (or CR template)
	Delete  = "rolloutDelete"  // delete pods one-by-one (STS safe way)
)

// Kind enumerates supported workload kinds in the work queue.
type Kind string

const (
	KindDeployment  Kind = "Deployment"
	KindStatefulSet Kind = "StatefulSet"
	KindDaemonSet   Kind = "DaemonSet"
	KindCR          Kind = "CustomResource"
)

type WorkItem struct {
	*WorkItemDryRun

	// Full objects for handlers that need them
	Dep *v1.Deployment
	Sts *v1.StatefulSet
	Ds  *v1.DaemonSet
	CR  *unstructured.Unstructured

	// Optional vendor bump for CRs (e.g., Strimzi)
	BumpAnnotationKey   string
	BumpAnnotationValue string
}

type WorkItemDryRun struct {
	Kind      Kind
	Namespace string
	Name      string

	// Strategy ties to your rollout strategy decision
	Strategy string
}

// Result now also carries an ordered queue.
type Result struct {
	// Ordered work queue built exactly in the order of `targets`
	Queue []WorkItem

	// Optional: quick stats for logs/metrics (no need to keep full grouped slices)
	Stats struct {
		Deployments, StatefulSets, DaemonSets, CustomResources int
	}
}

func (m *ManageRollout) SelectLinkerdDataPlane(ctx context.Context, obj *trv1alpha1.LinkerdTrustRotation) (*Result, error) {
	targets := obj.Spec.Rollout.TargetAnnotationSelector.Targets
	annotationKey := obj.Spec.Rollout.TargetAnnotationSelector.Key
	annotationValue := obj.Spec.Rollout.TargetAnnotationSelector.Value
	result := &Result{}

	for _, scope := range targets {
		namespaces := scope.AllowedNamespaces
		if len(namespaces) == 0 {
			return nil, fmt.Errorf("targets[%s]: allowedNamespaces is required", scope.KindType)
		}

		rolloutStrategy := scope.RolloutStrategy
		if len(rolloutStrategy) == 0 {
			rolloutStrategy = Restart
		}

		switch scope.KindType {
		case string(KindDaemonSet):
			var numDetections int
			for _, ns := range namespaces {
				var list v1.DaemonSetList
				if err := m.Client.List(ctx, &list, client.InNamespace(ns)); err != nil {
					return nil, fmt.Errorf("list daemonsets in %q: %w", ns, err)
				}

				for i := range list.Items {
					if hasAnnotationOnTemplate(list.Items[i].Spec.Template, annotationKey, annotationValue) {
						ds := *list.Items[i].DeepCopy()
						workItemDryRun := &WorkItemDryRun{
							Kind:      KindDaemonSet,
							Namespace: ds.Namespace,
							Name:      ds.Name,
							Strategy:  rolloutStrategy,
						}
						result.Queue = append(result.Queue, WorkItem{
							WorkItemDryRun: workItemDryRun,
							Ds:             &ds,
						})

						numDetections++
						result.Stats.DaemonSets++
					}
				}
			}

			m.Logger.Info(fmt.Sprintf("Found %d DaemonSets in namespaces %v",
				numDetections, namespaces))

		case string(KindDeployment):
			var numDetections int
			for _, ns := range namespaces {
				var list v1.DeploymentList
				if err := m.Client.List(ctx, &list, client.InNamespace(ns)); err != nil {
					return nil, fmt.Errorf("list deployments in %q: %w", ns, err)
				}

				for i := range list.Items {
					if hasAnnotationOnTemplate(list.Items[i].Spec.Template, annotationKey, annotationValue) {
						dep := *list.Items[i].DeepCopy()
						workItemDryRun := &WorkItemDryRun{
							Kind:      KindDeployment,
							Namespace: dep.Namespace,
							Name:      dep.Name,
							Strategy:  rolloutStrategy,
						}
						result.Queue = append(result.Queue, WorkItem{
							WorkItemDryRun: workItemDryRun,
							Dep:            &dep,
						})

						numDetections++
						result.Stats.Deployments++
					}
				}
			}

			m.Logger.Info(fmt.Sprintf("Found %d Deployments in namespaces %v",
				numDetections, namespaces))

		case string(KindCR):
			var numDetections int
			// CR: apiGroup/kind/version must be provided
			if len(scope.APIGroup) == 0 || len(scope.Kind) == 0 || len(scope.Version) == 0 {
				return nil, fmt.Errorf("targets[%s]: apiGroup, kind and version are required", scope.KindType)
			}

			gvk := schema.GroupVersionKind{Group: scope.APIGroup, Version: scope.Version, Kind: scope.Kind}

			for _, ns := range namespaces {
				ul := &unstructured.UnstructuredList{}
				ul.SetGroupVersionKind(gvk) // list will still work with UnstructuredList

				if err := m.Client.List(ctx, ul, client.InNamespace(ns)); err != nil {
					return nil, fmt.Errorf("list %s in %q: %w", gvk.String(), ns, err)
				}

				for i := range ul.Items {
					if crHasTemplateAnnotation(&ul.Items[i], annotationKey, annotationValue) {
						cr := *ul.Items[i].DeepCopy()
						workItemDryRun := &WorkItemDryRun{
							Kind:      KindCR,
							Namespace: cr.GetNamespace(),
							Name:      cr.GetName(),
							Strategy:  rolloutStrategy,
						}
						// If CR scope defines vendor-specific annotation bump, carry it
						crItem := WorkItem{
							WorkItemDryRun: workItemDryRun,
							CR:             &cr,
						}

						if scope.AnnotationBump != nil {
							crItem.BumpAnnotationKey = scope.AnnotationBump.BumpAnnotationKey
							crItem.BumpAnnotationValue = scope.AnnotationBump.BumpAnnotationValue
						}

						result.Queue = append(result.Queue, crItem)
						numDetections++
						result.Stats.CustomResources++
					}
				}
			}

			m.Logger.Info(fmt.Sprintf("Found %d Custom Resources in namespaces %v",
				numDetections, namespaces))

		case string(KindStatefulSet):
			var numDetections int
			for _, ns := range namespaces {
				var list v1.StatefulSetList
				if err := m.Client.List(ctx, &list, client.InNamespace(ns)); err != nil {
					return nil, fmt.Errorf("list statefulsets in %q: %w", ns, err)
				}

				sort.SliceStable(list.Items, func(i, j int) bool {
					return list.Items[i].Name < list.Items[j].Name
				})

				for i := range list.Items {
					if hasAnnotationOnTemplate(list.Items[i].Spec.Template, annotationKey, annotationValue) {
						sts := *list.Items[i].DeepCopy()
						workItemDryRun := &WorkItemDryRun{
							Kind:      KindStatefulSet,
							Namespace: sts.Namespace,
							Name:      sts.Name,
							Strategy:  rolloutStrategy,
						}
						result.Queue = append(result.Queue, WorkItem{
							WorkItemDryRun: workItemDryRun,
							Sts:            &sts,
						})

						numDetections++
						result.Stats.StatefulSets++
					}
				}
			}

			m.Logger.Info(fmt.Sprintf("Found %d StatefulSets in namespaces %v",
				numDetections, namespaces))

		default:
			return nil, fmt.Errorf("unsupported kind in targets: %s", scope.KindType)
		}

	}

	return result, nil
}

// crHasTemplateAnnotation checks common pod-template locations in CRDs for key=value.
func crHasTemplateAnnotation(u *unstructured.Unstructured, key, val string) bool {
	// spec.template.metadata.annotations
	if ann, ok := getAnno(u, "spec", "template", "metadata", "annotations"); ok {
		if v, ok := ann[key]; ok && (val == "" || v == val) {
			return true
		}
	}

	// spec.jobTemplate.spec.template.metadata.annotations (CronJob-like)
	if ann, ok := getAnno(u, "spec", "jobTemplate", "spec", "template", "metadata", "annotations"); ok {
		if v, ok := ann[key]; ok && (val == "" || v == val) {
			return true
		}
	}

	// spec.podTemplate.metadata.annotations (some operators)
	if ann, ok := getAnno(u, "spec", "podTemplate", "metadata", "annotations"); ok {
		if v, ok := ann[key]; ok && (val == "" || v == val) {
			return true
		}
	}

	// spec.pods[].metadata.annotations (StrimziPodSet-like)
	if arr, ok, _ := unstructured.NestedSlice(u.Object, "spec", "pods"); ok {
		for _, it := range arr {
			pm, _ := it.(map[string]any)
			if ann, ok := getAnnoFromMap(pm, "metadata", "annotations"); ok {
				if v, ok := ann[key]; ok && (val == "" || v == val) {
					return true
				}
			}
		}
	}

	return false
}

// getAnno fetches a map[string]string of annotations at a path from an Unstructured.
func getAnno(u *unstructured.Unstructured, path ...string) (map[string]string, bool) {
	return getAnnoFromMap(u.Object, path...)
}

func hasAnnotationOnTemplate(t corev1.PodTemplateSpec, key, val string) bool {
	if t.Annotations == nil {
		return false
	}

	v, ok := t.Annotations[key]
	return ok && v == val
}

// RestartLinkerdDataPlane bumps pod-template annotation for each CP deployment
// and waits until rollout is completed.
func (m *ManageRollout) RestartLinkerdDataPlane(ctx context.Context, obj *trv1alpha1.LinkerdTrustRotation) error {
	ltrSpec := obj.Spec
	result, err := m.SelectLinkerdDataPlane(ctx, obj)
	if err != nil {
		return err
	}

	if err := m.Status.SetPhase(ctx, obj,
		status.PhasePtr(trv1alpha1.PhaseRollingDP),
		status.ReasonPtr(trv1alpha1.ReasonDPBatchRestarting),
		status.StringPtr("Starting rollout restart Linkerd data plane"),
	); err != nil {
		return err
	}

	hash := planHash(result.Queue)
	total := len(result.Queue)
	start := 0
	if cur := obj.Status.Cursor; cur != nil && cur.PlanHash == hash && cur.Next > 0 && cur.Next <= total {
		start = cur.Next // resume
	} else {
		// init cursor
		if err := m.Status.SetPlanHash(ctx, obj, nil, 0, total, hash); err != nil {
			return err
		}
	}

	processed := start
	if err := m.Status.SetProgress(ctx, obj, true, &processed, &total); err != nil {
		return err
	}

	// helper to bump progress and persist
	bumpProgress := func(done WorkItem) error {
		processed++ // +1 per finished object
		// update cursor: next index and last done
		last := &trv1alpha1.WorkRef{
			Kind:      string(done.Kind),
			Namespace: getNamespace(done),
			Name:      getName(done),
		}
		if err := m.Status.SetPlanHash(ctx, obj, last, processed, total, hash); err != nil {
			return err
		}

		m.Logger.Info(fmt.Sprintf("Current progress: %d/%d", processed, total))
		return m.Status.SetProgress(ctx, obj, true, &processed, &total)
	}

	recordFailure := func(item WorkItem, cause error) error {
		// increment retry counter atomically using current status value
		retries := 0
		if obj.Status.Retries != nil {
			retries = obj.Status.Retries.Count
		}

		last := &trv1alpha1.WorkRef{
			Kind:      string(item.Kind),
			Namespace: getNamespace(item),
			Name:      getName(item),
		}
		if err := m.Status.SetRetry(ctx, obj, last, retries+1, cause.Error()); err != nil {
			return err
		}
		// do NOT advance cursor; resume from the same item next reconcile
		return cause
	}

	q := result.Queue
	for i := start; i < len(q); i++ {
		w := q[i]

		switch w.Kind {
		case KindDaemonSet:
			m.Logger.Info(fmt.Sprintf("Start linkerd data plane DaemonSet: %s/%s restarting",
				getNamespace(w), getName(w)))

			if err := m.bumpRestartAnnotation(ctx, w.Ds); err != nil {
				return recordFailure(w, err)
			}

			if err := m.waitDaemonSetRolledOut(ctx, getNamespaced(w), rolloutPerLimit); err != nil {
				return recordFailure(w, err)
			}

			if err := m.runProxyCheckIfEnabled(ctx, &ltrSpec, getNamespace(w), getName(w), rolloutPerLimit); err != nil {
				return recordFailure(w, err)
			}

			if err := bumpProgress(w); err != nil {
				return recordFailure(w, err)
			}

			m.Logger.Info(fmt.Sprintf("Restarted linkerd data plane DaemonSet: %s/%s",
				getNamespace(w), getName(w)))

		case KindDeployment:
			m.Logger.Info(fmt.Sprintf("Start linkerd data plane Deployment: %s/%s restarting",
				getNamespace(w), getName(w)))

			if err := m.bumpRestartAnnotation(ctx, w.Dep); err != nil {
				return recordFailure(w, err)
			}

			if err := m.waitDeploymentRolledOut(ctx, getNamespaced(w), rolloutPerLimit); err != nil {
				return recordFailure(w, err)
			}

			if err := m.runProxyCheckIfEnabled(ctx, &ltrSpec, getNamespace(w), getName(w), rolloutPerLimit); err != nil {
				return recordFailure(w, err)
			}

			if err := bumpProgress(w); err != nil {
				return recordFailure(w, err)
			}

			m.Logger.Info(fmt.Sprintf("Restarted linkerd data plane Deployment: %s/%s",
				getNamespace(w), getName(w)))

		case KindCR:
			m.Logger.Info(fmt.Sprintf("Start linkerd data plane Custom Resource: %s/%s restarting",
				getNamespace(w), getName(w)))

			if len(w.BumpAnnotationKey) == 0 || len(w.BumpAnnotationValue) == 0 {
				return recordFailure(w, fmt.Errorf(
					"BumpAnnotationKey, BumpAnnotationValue is required for custom resources %s", w.CR.GetKind()))
			}

			if err := m.bumpAnnotationGeneric(ctx, w.CR, w.BumpAnnotationKey, w.BumpAnnotationValue); err != nil {
				return recordFailure(w, err)
			}

			if err := m.waitCRByAnnotationAndStatus(ctx, getNamespaced(w), w.CR, w.BumpAnnotationKey,
				true, rolloutPerLimit); err != nil {
				return recordFailure(w, err)
			}

			if err := m.runProxyCheckIfEnabled(ctx, &ltrSpec, getNamespace(w), getName(w), rolloutPerLimit); err != nil {
				return recordFailure(w, err)
			}

			if err := bumpProgress(w); err != nil {
				return recordFailure(w, err)
			}

			m.Logger.Info(fmt.Sprintf("Restarted linkerd data plane Custom Resource: %s/%s",
				getNamespace(w), getName(w)))

		case KindStatefulSet:
			m.Logger.Info(fmt.Sprintf("Start linkerd data plane StatefulSet: %s/%s restarting",
				getNamespace(w), getName(w)))

			if w.Strategy == Restart {
				if err := m.bumpRestartAnnotation(ctx, w.Sts); err != nil {
					return recordFailure(w, err)
				}

				if err := m.waitStatefulSetRolledOut(ctx, getNamespaced(w), rolloutPerLimit); err != nil {
					return recordFailure(w, err)
				}
			}

			if w.Strategy == Delete {
				if err := m.restartStatefulSetByDelete(ctx, w.Sts, rolloutPerLimit); err != nil {
					return recordFailure(w, err)
				}
			}

			if err := m.runProxyCheckIfEnabled(ctx, &ltrSpec, getNamespace(w), getName(w), rolloutPerLimit); err != nil {
				return recordFailure(w, err)
			}

			if err := bumpProgress(w); err != nil {
				return recordFailure(w, err)
			}

			m.Logger.Info(fmt.Sprintf("Restarted linkerd data plane StatefulSet: %s/%s",
				w.Sts.Namespace, w.Sts.Name))
		}
	}

	if err := m.Status.SetPhase(ctx, obj,
		status.PhasePtr(trv1alpha1.PhaseRollingDP),
		status.ReasonPtr(trv1alpha1.ReasonDPThresholdReached),
		status.StringPtr("Finished restarted Linkerd data plane"),
	); err != nil {
		return err
	}

	if err := m.Status.SetRetry(ctx, obj, nil, 0, ""); err != nil {
		return err
	}

	return m.Status.SetPlanHash(ctx, obj, nil, 0, total, hash)
}

// runProxyCheckIfEnabled runs `linkerd check --proxy` for the given workload
// only if Safety.LinkerdCheckProxy is enabled.
func (m *ManageRollout) runProxyCheckIfEnabled(
	ctx context.Context,
	spec *trv1alpha1.LinkerdTrustRotationSpec,
	targetNS, targetName string,
	timeout time.Duration,
) error {
	if !spec.Safety.LinkerdCheckProxy {
		return nil
	}

	return m.runLinkerdCheckJob(ctx, NewCheckProxyOptions(
		false,
		spec.Safety.LinkerdCheckProxyImage,
		targetNS,
		spec.Namespace,
		targetName,
		timeout,
	))
}
