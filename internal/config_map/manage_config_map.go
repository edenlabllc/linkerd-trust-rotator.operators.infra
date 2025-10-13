package config_map

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"sort"
	"strings"

	"github.com/go-logr/logr"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"linkerd-trust-rotator.operators.infra/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	trv1alpha1 "linkerd-trust-rotator.operators.infra/api/v1alpha1"
)

const (
	configMapDataKey = "ca-bundle.crt"
)

type ManageConfigMap struct {
	Client client.Client
	Scheme *runtime.Scheme
	Logger logr.Logger
}

// New returns a new configMap manager.
func New(c client.Client, s *runtime.Scheme, l logr.Logger) *ManageConfigMap {
	return &ManageConfigMap{Client: c, Scheme: s, Logger: l.WithName("ConfigMap")}
}

type Result struct {
	Certs []*x509.Certificate
	Fps   []string
	State v1alpha1.BundleState
}

// LoadAndInspectCMBundle fetches the ConfigMap and inspects the ca-bundle.crt.
// It returns parsed certs, their SHA-256 fingerprints, and the BundleState.
func (m *ManageConfigMap) LoadAndInspectCMBundle(ctx context.Context, obj *trv1alpha1.LinkerdTrustRotation) (*Result, error) {
	var state v1alpha1.BundleState

	cm := &v1.ConfigMap{}
	cmNamespaced := types.NamespacedName{Namespace: obj.Spec.Namespace, Name: obj.Spec.TrustRootsConfigMap}
	if err := m.Client.Get(ctx, cmNamespaced, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("configmap %s not found", cmNamespaced.String())
		}

		return nil, fmt.Errorf("get configmap %s: %w", cmNamespaced.String(), err)
	}

	raw, ok := cm.Data[configMapDataKey]
	if !ok {
		return nil, fmt.Errorf("configmap %s has no key %q", cmNamespaced.String(), configMapDataKey)
	}

	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("configmap %s key %q is empty", cmNamespaced.String(), configMapDataKey)
	}

	certs, err := parsePEMCerts([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("parse bundle: %w", err)
	}

	fps := make([]string, 0, len(certs))
	for _, c := range certs {
		sum := sha256.Sum256(c.Raw)
		fps = append(fps, strings.ToLower(hex.EncodeToString(sum[:])))
	}
	// sort fingerprints for stable comparisons
	sort.Strings(fps)

	switch len(certs) {
	case 0:
		return nil, fmt.Errorf("no CERTIFICATE blocks found in %s/%s", cmNamespaced.Namespace, cmNamespaced.Name)
	case 1:
		state = v1alpha1.BundleStateSingle
	default:
		state = v1alpha1.BundleStateOverlap
	}

	return &Result{Certs: certs, Fps: fps, State: state}, nil
}

// parsePEMCerts extracts all x509 CERTIFICATE blocks from a PEM bundle.
func parsePEMCerts(pemBytes []byte) ([]*x509.Certificate, error) {
	var (
		out  []*x509.Certificate
		rest = pemBytes
	)
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse x509 cert: %w", err)
		}
		out = append(out, cert)
	}
	return out, nil
}
