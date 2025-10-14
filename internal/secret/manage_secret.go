package secret

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	trv1alpha1 "linkerd-trust-rotator.operators.infra/api/v1alpha1"
)

const (
	secretAnnotation = "trust-anchor.linkerd.edenlab.io/created"
	secretDataKey    = "tls.crt"
)

type ManageSecret struct {
	Client client.Client
	Scheme *runtime.Scheme
	Logger logr.Logger
}

// New returns a new secret manager.
func New(c client.Client, s *runtime.Scheme, l logr.Logger) *ManageSecret {
	return &ManageSecret{Client: c, Scheme: s, Logger: l.WithName("Secrets")}
}

// Result represents the outcome of EnsureTrustSecrets.
type Result struct {
	// CreatedPrevious indicates whether the previous secret was created (bootstrap).
	CreatedPrevious bool
	// CurrentFP is the SHA-256 fingerprint of current secret certificate bundle.
	CurrentFP string
	// PreviousFP is the SHA-256 fingerprint of previous secret certificate bundle (empty if not available).
	PreviousFP string
	// Diverged is true when both fingerprints are available and differ.
	Diverged bool
}

// EnsureTrustSecrets validates the current secret, optionally bootstraps the previous secret,
// and returns fingerprints to drive rotation logic.
// - current must exist and contain a certificate under one of certKeys (e.g., "tls.crt", "ca.crt").
// - if previous is missing and bootstrapPrevious is true, it is created as a byte-for-byte copy of current.
// - this function NEVER overwrites an existing previous secret.
// - fingerprints are computed from all CERTIFICATE PEM blocks by concatenating DER and hashing with SHA-256.
func (m *ManageSecret) EnsureTrustSecrets(ctx context.Context, obj *trv1alpha1.LinkerdTrustRotation) (*Result, error) {
	var errFP error
	result := &Result{}

	cNamespaced := types.NamespacedName{Namespace: obj.Spec.Linkerd.Namespace, Name: obj.Spec.Linkerd.TrustAnchorSecret}
	cSecret := &v1.Secret{}
	if err := m.Client.Get(ctx, cNamespaced, cSecret); err != nil {
		return nil, err
	}

	pNamespaced := types.NamespacedName{Namespace: obj.Spec.Linkerd.Namespace, Name: obj.Spec.Linkerd.PreviousTrustAnchorSecret}
	pSecret := &v1.Secret{}
	if err := m.Client.Get(ctx, pNamespaced, pSecret); err != nil {
		if apierrors.IsNotFound(err) && obj.Spec.Linkerd.BootstrapPreviousSecret {
			if err := m.bootstrapPreviousSecrets(ctx, cSecret, obj); err != nil {
				return nil, err
			}

			var (
				duration = 3 * time.Second
				interval = 1 * time.Second
			)

			for start := time.Now(); ; {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}

				if err := m.Client.Get(ctx, pNamespaced, pSecret); err != nil {
					if !apierrors.IsNotFound(err) {
						return nil, err
					}
				} else {
					break
				}

				if time.Since(start) >= duration {
					return nil, fmt.Errorf("timeout waiting for %s", pNamespaced.String())
				}

				time.Sleep(interval)
			}

			m.Logger.Info(fmt.Sprintf("bootstrapped previous secret from %s", cSecret.Name))
		} else {
			return nil, err
		}
	}

	result.CurrentFP, errFP = fingerprintPEMCerts(cSecret.Data[secretDataKey])
	if errFP != nil {
		return nil, errFP
	}

	result.PreviousFP, errFP = fingerprintPEMCerts(pSecret.Data[secretDataKey])
	if errFP != nil {
		return nil, errFP
	}

	if pSecret.Annotations[secretAnnotation] == "true" {
		result.CreatedPrevious = true
	}

	// cmp options:
	// - EquateEmpty: treat nil and empty maps as equal.
	// - SortMaps is not required for map[string][]byte; cmp handles map order.
	result.Diverged = !cmp.Equal(cSecret.Data, pSecret.Data, []cmp.Option{cmpopts.EquateEmpty()}...)

	return result, nil
}

func fingerprintPEMCerts(pemBytes []byte) (string, error) {
	der, err := concatDER(pemBytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func concatDER(pemBytes []byte) ([]byte, error) {
	var out []byte
	in := pemBytes
	for {
		block, rest := pem.Decode(in)
		if block == nil {
			break
		}
		in = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return nil, fmt.Errorf("invalid certificate in PEM: %w", err)
		}
		out = append(out, block.Bytes...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no CERTIFICATE blocks found")
	}
	return out, nil
}

func (m *ManageSecret) bootstrapPreviousSecrets(ctx context.Context, cSecret *v1.Secret, obj *trv1alpha1.LinkerdTrustRotation) error {
	previousSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      obj.Spec.Linkerd.PreviousTrustAnchorSecret,
			Namespace: obj.Spec.Linkerd.Namespace,
			Annotations: map[string]string{
				secretAnnotation: "true",
			},
		},
		Type: v1.SecretTypeTLS,
		Data: cSecret.Data,
	}

	return m.Client.Create(ctx, previousSecret)
}

func (m *ManageSecret) DeleteSecrets(ctx context.Context, obj *trv1alpha1.LinkerdTrustRotation, name string) error {
	var (
		zero      int64 = 0
		bg              = metav1.DeletePropagationBackground
		namespace       = obj.Spec.Linkerd.Namespace
	)

	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}

	// Immediate delete (no grace), background propagation (default is fine for Secret, but explicit is clearer)
	if err := m.Client.Delete(ctx, secret, &client.DeleteOptions{
		GracePeriodSeconds: &zero,
		PropagationPolicy:  &bg,
	}); err != nil {
		if apierrors.IsNotFound(err) {
			// Treat as success if it's already gone
			m.Logger.Info(fmt.Sprintf("Secret %s/%s already deleted", namespace, name))
			return nil
		}

		return fmt.Errorf("delete secret %s/%s: %w", namespace, name, err)
	}

	return nil
}
