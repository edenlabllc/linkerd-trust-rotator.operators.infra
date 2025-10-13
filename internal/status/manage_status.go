package status

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	trv1alpha1 "linkerd-trust-rotator.operators.infra/api/v1alpha1"
)

// ManageStatus updates CR status via the status subresource.
type ManageStatus struct {
	Client client.Client
	Scheme *runtime.Scheme
	Logger logr.Logger
}

// New returns a new status manager.
func New(c client.Client, r *runtime.Scheme, l logr.Logger) *ManageStatus {
	return &ManageStatus{Client: c, Scheme: r, Logger: l.WithName("Status")}
}

// cmp options for comparing LinkerdTrustRotationStatus objects.
// We ignore volatile fields like LastUpdated to avoid infinite patches.
func statusCmpOptions() []cmp.Option {
	return []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(trv1alpha1.LinkerdTrustRotationStatus{}, "LastUpdated"),
	}
}

// BundlePtr returns a pointer to the given bundle.
func BundlePtr(b trv1alpha1.BundleState) *trv1alpha1.BundleState { return &b }

// StringPtr returns a pointer to the given string.
func StringPtr(s string) *string { return &s }

// PhasePtr returns a pointer to the given Phase.
func PhasePtr(p trv1alpha1.Phase) *trv1alpha1.Phase { return &p }

// ReasonPtr returns a pointer to the given Reason.
func ReasonPtr(r trv1alpha1.Reason) *trv1alpha1.Reason { return &r }

// Patch mutates the status with the provided function and patches it using MergeFrom.
// Caller must pass a live object (fetched from the API).
func (m *ManageStatus) Patch(ctx context.Context, obj *trv1alpha1.LinkerdTrustRotation, processName string, mutate func(st *trv1alpha1.LinkerdTrustRotationStatus)) error {
	// Base for merge patch
	oldObj := obj.DeepCopy()

	// Deep snapshot of BEFORE
	beforePtr := obj.Status.DeepCopy()
	after := *beforePtr.DeepCopy()

	mutate(&after)

	// Compare with cmp, ignoring volatile fields
	if cmp.Equal(*beforePtr, after, statusCmpOptions()...) {
		// No meaningful change — skip patch
		return nil
	}

	// Apply mutated status and set LastUpdated only when there are changes
	now := metav1.NewTime(time.Now().UTC())
	after.LastUpdated = &now
	obj.Status = after
	m.Logger.Info(fmt.Sprintf("%s, patching status", processName))
	return m.Client.Status().Patch(ctx, obj, client.MergeFrom(oldObj))
}

// SetPhase sets the high-level phase, with optional reason/message.
func (m *ManageStatus) SetPhase(ctx context.Context, obj *trv1alpha1.LinkerdTrustRotation,
	phase *trv1alpha1.Phase, reason *trv1alpha1.Reason, message *string) error {
	return m.Patch(ctx, obj, "SetPhase", func(st *trv1alpha1.LinkerdTrustRotationStatus) {
		st.Phase = phase
		st.Reason = reason
		st.Message = message
	})
}

// SetProgress sets control-plane ready and data-plane percentage.
func (m *ManageStatus) SetProgress(ctx context.Context, obj *trv1alpha1.LinkerdTrustRotation, cpReady bool, current, total *int) error {
	percent := 0
	if current != nil && total != nil {
		percent = calcPercent(*current, *total)
	}
	return m.Patch(ctx, obj, "SetProgress", func(st *trv1alpha1.LinkerdTrustRotationStatus) {
		st.Progress = &trv1alpha1.ProgressStatus{
			ControlPlaneReady: cpReady,
			DataPlanePercent:  percent,
		}
	})
}

func calcPercent(current, total int) int {
	if total <= 0 {
		return 0
	}

	if current < 0 {
		current = 0
	}

	if current > total {
		current = total
	}
	// round to nearest integer
	p := int(math.Round(float64(current) * 100.0 / float64(total)))
	if p < 0 {
		return 0
	}

	if p > 100 {
		return 100
	}

	return p
}

// SetTrustInfo sets bundle state and fingerprints.
func (m *ManageStatus) SetTrustInfo(ctx context.Context, obj *trv1alpha1.LinkerdTrustRotation, bundleState *trv1alpha1.BundleState, currentFP, previousFP string) error {
	return m.Patch(ctx, obj, "SetTrustInfo", func(st *trv1alpha1.LinkerdTrustRotationStatus) {
		st.Trust = &trv1alpha1.TrustStatus{
			BundleState: bundleState,
			CurrentFP:   currentFP,
			PreviousFP:  previousFP,
		}
	})
}

// SetPlanHash updates plan hash state.
func (m *ManageStatus) SetPlanHash(ctx context.Context, obj *trv1alpha1.LinkerdTrustRotation, workRef *trv1alpha1.WorkRef, next, total int, hash string) error {
	return m.Patch(ctx, obj, "SetPlanHash", func(st *trv1alpha1.LinkerdTrustRotationStatus) {
		st.Cursor = &trv1alpha1.RolloutCursor{
			PlanHash: hash,
			Next:     next,
			Total:    total,
			LastDone: workRef,
		}
	})
}

// SetRetry updates retry counters and last error.
func (m *ManageStatus) SetRetry(ctx context.Context, obj *trv1alpha1.LinkerdTrustRotation, workRef *trv1alpha1.WorkRef, count int, lastErr string) error {
	now := metav1.Time{}
	if count > 0 && len(lastErr) > 0 {
		now = metav1.NewTime(time.Now().UTC())
	}

	return m.Patch(ctx, obj, "SetRetry", func(st *trv1alpha1.LinkerdTrustRotationStatus) {
		st.Retries = &trv1alpha1.RetryStatus{
			Count:         count,
			LastError:     lastErr,
			LastFailed:    workRef,
			LastErrorTime: &now,
		}
	})
}

// SetDryRunOutput sets the human-readable output of the last dry run.
func (m *ManageStatus) SetDryRunOutput(ctx context.Context, obj *trv1alpha1.LinkerdTrustRotation, dryRunOutput string) error {
	return m.Patch(ctx, obj, "SetDryRunOutput", func(st *trv1alpha1.LinkerdTrustRotationStatus) {
		st.Phase = PhasePtr(trv1alpha1.PhaseDryRun)
		st.Reason = ReasonPtr(trv1alpha1.ReasonDryRun)
		st.Message = StringPtr("The data-plane dry run has completed successfully")
		st.DryRunOutput = dryRunOutput
		st.Progress = nil
	})
}

// MarkSucceeded marks completion and sets Succeeded phase.
func (m *ManageStatus) MarkSucceeded(ctx context.Context, obj *trv1alpha1.LinkerdTrustRotation, message string) error {
	now := metav1.NewTime(time.Now().UTC())
	return m.Patch(ctx, obj, "MarkSucceeded", func(st *trv1alpha1.LinkerdTrustRotationStatus) {
		st.Phase = PhasePtr("Succeeded")
		st.Reason = ReasonPtr("Completed")
		st.Message = &message
		st.CompletionTime = &now
	})
}

// MarkFailed marks completion and sets Failed phase with reason/message.
func (m *ManageStatus) MarkFailed(ctx context.Context, obj *trv1alpha1.LinkerdTrustRotation,
	reason trv1alpha1.Reason, message string) error {
	now := metav1.NewTime(time.Now().UTC())
	return m.Patch(ctx, obj, "MarkFailed", func(st *trv1alpha1.LinkerdTrustRotationStatus) {
		st.Phase = PhasePtr("Failed")
		st.Reason = &reason
		st.Message = &message
		st.CompletionTime = &now
	})
}
