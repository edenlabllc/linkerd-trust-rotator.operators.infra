package rollout

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

func ptrInt32(v int32) *int32 { return &v }

func getNamespaced(w WorkItem) types.NamespacedName {
	return types.NamespacedName{
		Namespace: getNamespace(w),
		Name:      getName(w),
	}
}

func getNamespace(w WorkItem) string {
	switch w.Kind {
	case KindDeployment:
		return w.Dep.Namespace
	case KindStatefulSet:
		return w.Sts.Namespace
	case KindDaemonSet:
		return w.Ds.Namespace
	case KindCR:
		return w.CR.GetNamespace()
	default:
		return ""
	}
}

func getName(w WorkItem) string {
	switch w.Kind {
	case KindDeployment:
		return w.Dep.Name
	case KindStatefulSet:
		return w.Sts.Name
	case KindDaemonSet:
		return w.Ds.Name
	case KindCR:
		return w.CR.GetName()
	default:
		return ""
	}
}

// getAnnoFromMap is the same but starts from a generic map.
func getAnnoFromMap(m map[string]any, path ...string) (map[string]string, bool) {
	cur := m
	for _, p := range path[:len(path)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}

	last := path[len(path)-1]
	raw, ok := cur[last].(map[string]any)
	if !ok {
		return nil, false
	}

	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}

	return out, true
}

func podOrdinal(name string) int {
	// expects NAME-<ordinal>
	n := -1
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '-' {
			_, _ = fmt.Sscanf(name[i+1:], "%d", &n)
			break
		}
	}

	return n
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}

	return false
}

// planHash returns a stable hash of the rollout queue.
func planHash(queue []WorkItem) string {
	h := sha1.New()
	for _, w := range queue {
		// only stable identifiers — no volatile fields
		_, _ = fmt.Fprintf(h, "%s|", string(w.Kind))

		switch w.Kind {
		case KindDeployment:
			_, _ = fmt.Fprintf(h, "%s/%s", w.Dep.Namespace, w.Dep.Name)
		case KindStatefulSet:
			_, _ = fmt.Fprintf(h, "%s/%s|%s", w.Sts.Namespace, w.Sts.Name, w.Strategy)
		case KindDaemonSet:
			_, _ = fmt.Fprintf(h, "%s/%s", w.Ds.Namespace, w.Ds.Name)
		case KindCR:
			_, _ = fmt.Fprintf(h, "%s/%s|%s", w.CR.GetNamespace(), w.CR.GetName(), w.Strategy)
			if w.BumpAnnotationKey != "" {
				_, _ = fmt.Fprintf(h, "|%s=%s", w.BumpAnnotationKey, w.BumpAnnotationValue)
			}
		}

		_, _ = fmt.Fprintf(h, "\n")
	}

	return hex.EncodeToString(h.Sum(nil))[:12] // short, but stable
}
