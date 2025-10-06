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

	// PhaseBootstrap — previous secret created/verified (first initialization)
	PhaseBootstrap Phase = "Bootstrap"

	// PhasePreCheck — running pre-checks (optional linkerd check --proxy, quotas, availability)
	PhasePreCheck Phase = "PreCheck"

	// PhaseRollingCP — restarting control plane
	PhaseRollingCP Phase = "RollingCP"

	// PhaseRollingDP — restarting data plane (via annotation selector)
	PhaseRollingDP Phase = "RollingDP"

	// PhaseVerifying — verifying dataplane readiness threshold
	PhaseVerifying Phase = "Verifying"

	// PhaseHold — waiting before cleanup (holdBeforeCleanup timer)
	PhaseHold Phase = "Hold"

	// PhaseCleanup — deleting old previous secret, finalizing bundle
	PhaseCleanup Phase = "Cleanup"

	// PhaseSucceeded — rotation finished successfully
	PhaseSucceeded Phase = "Succeeded"

	// PhaseFailed — rotation failed (exceeded maxFailures, timeouts, etc.)
	PhaseFailed Phase = "Failed"

	// PhasePaused — manually paused (if pause flag in spec is set)
	PhasePaused Phase = "Paused"
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

	// --- RollingCP ---
	ReasonCPRestarting Reason = "ControlPlaneRestarting"
	ReasonCPReady      Reason = "ControlPlaneReady"

	// --- RollingDP ---
	ReasonDPBatchRestarting  Reason = "DataPlaneBatchRestarting"
	ReasonDPThresholdReached Reason = "DataPlaneThresholdReached"

	// --- Verifying ---
	ReasonVerificationPassed Reason = "VerificationPassed"
	ReasonVerificationFailed Reason = "VerificationFailed"

	// --- Hold ---
	ReasonHoldTimerRunning Reason = "HoldTimerRunning"

	// --- Cleanup ---
	ReasonPreviousDeleted Reason = "PreviousSecretDeleted"

	// --- Terminal ---
	ReasonRotationSucceeded Reason = "RotationSucceeded"
	ReasonRotationFailed    Reason = "RotationFailed"
	ReasonManuallyPaused    Reason = "ManuallyPaused"
)
