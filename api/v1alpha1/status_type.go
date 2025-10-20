package v1alpha1

// BundleState represents the state of the trust anchor bundle
// stored in the trust-manager ConfigMap.
//
// Possible states:
// - single  — bundle contains only the current trust anchor
// - overlap — bundle contains both current and previous anchors (rotation in progress)
type BundleState string

const (
	// BundleStateSingle means the bundle contains only the current trust anchor.
	// This is the steady-state after cleanup.
	BundleStateSingle BundleState = "single"

	// BundleStateOverlap means the bundle contains both the current and the previous trust anchors.
	// This state occurs during rotation, allowing workloads to trust both old and new certificates.
	BundleStateOverlap BundleState = "overlap"
)

// Phase represents the lifecycle phase of a LinkerdTrustRotation
type Phase string

const (
	// PhaseIdle — no changes detected, everything is fine (default)
	PhaseIdle Phase = "Idle"

	// PhaseDetecting — trust roots change detected (ConfigMap/Secrets analyzed)
	PhaseDetecting Phase = "Detecting"

	// PhaseDryRun indicates a data-plane dry run (plan/validate) with no changes applied
	PhaseDryRun Phase = "DryRun"

	// PhaseBootstrap — previous secret created/verified (first initialization)
	PhaseBootstrap Phase = "Bootstrap"

	// PhasePreCheck — running pre-checks (optional linkerd check --proxy, quotas, availability)
	PhasePreCheck Phase = "PreCheck"

	// PhaseRollingControlPlane — restarting control plane
	PhaseRollingControlPlane Phase = "RollingCP"

	// PhaseRollingDataPlane — restarting data plane (via annotation selector)
	PhaseRollingDataPlane Phase = "RollingDP"

	// PhaseVerifying — verifying data plane readiness threshold
	PhaseVerifying Phase = "Verifying"

	// PhaseHold — waiting after cleanup (holdAfterCleanup timer)
	PhaseHold Phase = "Hold"

	// PhaseCleanup — deleting old previous secret, finalizing bundle
	PhaseCleanup Phase = "Cleanup"

	// PhaseSucceeded — rotation finished successfully
	PhaseSucceeded Phase = "Succeeded"

	// PhaseFailed — rotation failed (exceeded maxFailures, timeouts, etc.)
	PhaseFailed Phase = "Failed"
)

// Reason is a short, machine-readable identifier that explains
// why the object entered the current Phase.
type Reason string

const (
	// --- Detecting ---
	ReasonConfigMapChanged Reason = "ConfigMapChanged"
	ReasonSecretsDiverged  Reason = "SecretsDiverged"

	// --- Bootstrap ---
	ReasonPreviousCreated   Reason = "PreviousSecretCreated"
	ReasonPreviousValidated Reason = "PreviousSecretValidated"

	// --- PreCheck ---
	ReasonProxyCheckFailed   Reason = "ProxyCheckFailed"
	ReasonMaxRetriesExceeded Reason = "ReasonMaxRetriesExceeded"

	// --- RollingControlPlane ---
	ReasonControlPlaneRestarting Reason = "ControlPlaneRestarting"
	ReasonControlPlaneReady      Reason = "ControlPlaneReady"

	// --- RollingDataPlane ---
	ReasonDataPlaneBatchRestarting  Reason = "DataPlaneBatchRestarting"
	ReasonDataPlaneThresholdReached Reason = "DataPlaneThresholdReached"

	// --- Verifying ---
	ReasonVerificationSucceeded Reason = "VerificationSucceeded"
	ReasonVerificationFailed    Reason = "VerificationFailed"

	// --- Hold ---
	ReasonHoldTimerRunning Reason = "HoldTimerRunning"

	// --- Cleanup ---
	ReasonPreviousDeleted Reason = "PreviousSecretDeleted"

	// --- Result ---
	ReasonRotationSucceeded Reason = "RotationSucceeded"
	ReasonRotationFailed    Reason = "RotationFailed"

	// --- DryRun ---
	ReasonDryRun Reason = "DryRunCompleted"
)
