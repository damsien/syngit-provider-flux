package fluxprovider

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	helmprovider "github.com/syngit-org/syngit-provider-helm/pkg"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// DefaultInterval is the reconciliation interval applied to the generated HelmRelease.
const DefaultInterval = 10 * time.Minute

// ConvertToHelmRelease decodes a Helm release secret with the helm provider's
// ExtractRelease and produces a Flux v2 HelmRelease custom resource. The
// release name, namespace, chart name/version, and user-supplied values are
// derived from the secret. The chart sourceRef is not stored in the Helm release
// secret and must be supplied by the caller.
func ConvertToHelmRelease(secret *corev1.Secret, sourceRef helmv2.CrossNamespaceObjectReference) (*FluxHelmRelease, error) {
	rel, err := helmprovider.ExtractRelease(secret)
	if err != nil {
		return nil, fmt.Errorf("failed to extract release from helm secret: %w", err)
	}

	chartSpec := helmv2.HelmChartTemplateSpec{SourceRef: sourceRef}
	if rel.Chart != nil && rel.Chart.Metadata != nil {
		chartSpec.Chart = rel.Chart.Metadata.Name
		chartSpec.Version = rel.Chart.Metadata.Version
	}

	rawValues, err := marshalValues(rel.Config)
	if err != nil {
		return nil, err
	}

	hr := &helmv2.HelmRelease{
		TypeMeta: metav1.TypeMeta{
			APIVersion: helmv2.GroupVersion.String(),
			Kind:       helmv2.HelmReleaseKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      rel.Name,
			Namespace: rel.Namespace,
		},
		Spec: helmv2.HelmReleaseSpec{
			Interval:        metav1.Duration{Duration: DefaultInterval},
			ReleaseName:     rel.Name,
			TargetNamespace: rel.Namespace,
			Chart: &helmv2.HelmChartTemplate{
				Spec: chartSpec,
			},
			Values: rawValues,
		},
	}

	rawYAML, err := yaml.Marshal(hr)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal HelmRelease to YAML: %w", err)
	}

	rawYAMLWithHeader := string(rawYAML)

	return &FluxHelmRelease{
		HelmRelease: hr,
		RawYAML:     rawYAMLWithHeader,
	}, nil
}

// ConvertToHelmReleaseWithExisting decodes a Helm release secret and produces a
// Flux v2 HelmRelease that is a copy of the supplied existing HelmRelease with
// only spec.values replaced by the secret's user-supplied values. Every other
// field (chart, sourceRef, interval, install/upgrade options, metadata, ...) is
// preserved exactly. An empty/absent config in the secret clears spec.values.
// The input is not mutated.
func ConvertToHelmReleaseWithExisting(secret *corev1.Secret, existing *helmv2.HelmRelease) (*FluxHelmRelease, error) {
	if existing == nil {
		return nil, fmt.Errorf("existing HelmRelease must not be nil")
	}

	rel, err := helmprovider.ExtractRelease(secret)
	if err != nil {
		return nil, fmt.Errorf("failed to extract release from helm secret: %w", err)
	}

	rawValues, err := marshalValues(rel.Config)
	if err != nil {
		return nil, err
	}

	hr := existing.DeepCopy()
	hr.Spec.Values = rawValues

	rawYAML, err := yaml.Marshal(hr)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal HelmRelease to YAML: %w", err)
	}

	return &FluxHelmRelease{
		HelmRelease: hr,
		RawYAML:     string(rawYAML),
	}, nil
}

// marshalValues converts the structured values map into the apiextensionsv1.JSON
// wrapper used by HelmReleaseSpec.Values. Returns nil for empty input so the
// field is omitted from the serialized HelmRelease.
func marshalValues(values map[string]interface{}) (*apiextensionsv1.JSON, error) {
	if len(values) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal values to JSON: %w", err)
	}
	return &apiextensionsv1.JSON{Raw: raw}, nil
}

const (
	fluxHelmGroup   = "helm.toolkit.fluxcd.io"
	helmReleaseKind = "HelmRelease"
)

// IsCorrectHelmRelease reports whether the object is a Flux HelmRelease whose name
// (and namespace, when the manifest declares one) matches the given ones.
func IsCorrectHelmRelease(object map[string]interface{}, name, namespace string) bool {
	apiVersion, _ := object["apiVersion"].(string)
	kind, _ := object["kind"].(string)
	if kind != helmReleaseKind || !strings.HasPrefix(apiVersion, fluxHelmGroup+"/") {
		return false
	}
	md, _ := object["metadata"].(map[string]interface{})
	n, _ := md["name"].(string)
	ns, _ := md["namespace"].(string)
	if n != name {
		return false
	}
	return (ns == "" && namespace == "") || ns == namespace
}

// ExtractSourceRefFromHelmRelease reads spec.chart.spec.sourceRef from a HelmRelease object
// (decoded as a generic map) into a CrossNamespaceObjectReference. The boolean
// is false when the sourceRef is absent or has no name.
func ExtractSourceRefFromHelmRelease(obj map[string]interface{}) (helmv2.CrossNamespaceObjectReference, bool) {
	sr, found, err := unstructured.NestedMap(obj, "spec", "chart", "spec", "sourceRef")
	if err != nil || !found {
		return helmv2.CrossNamespaceObjectReference{}, false
	}
	ref := helmv2.CrossNamespaceObjectReference{}
	ref.APIVersion, _ = sr["apiVersion"].(string)
	ref.Kind, _ = sr["kind"].(string)
	ref.Name, _ = sr["name"].(string)
	ref.Namespace, _ = sr["namespace"].(string)
	if ref.Name == "" {
		return helmv2.CrossNamespaceObjectReference{}, false
	}
	return ref, true
}
