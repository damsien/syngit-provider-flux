package fluxprovider

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

const helmSecretType = "helm.sh/release.v1"

// releaseEnvelope mirrors the subset of the helm v4 Release JSON that the
// helm provider's ExtractRelease decodes for our tests.
type releaseEnvelope struct {
	Name      string                 `json:"name"`
	Namespace string                 `json:"namespace"`
	Config    map[string]interface{} `json:"config,omitempty"`
	Chart     *chartEnvelope         `json:"chart,omitempty"`
}

type chartEnvelope struct {
	Metadata *chartMetadata `json:"metadata,omitempty"`
}

type chartMetadata struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

func encodeRelease(rel *releaseEnvelope) ([]byte, error) {
	jsonData, err := json.Marshal(rel)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(jsonData); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}

	return []byte(base64.StdEncoding.EncodeToString(buf.Bytes())), nil
}

func validHelmSecret(t *testing.T, releaseName, namespace, chartName, chartVersion string, config map[string]interface{}) *corev1.Secret {
	t.Helper()

	env := &releaseEnvelope{
		Name:      releaseName,
		Namespace: namespace,
		Config:    config,
	}
	if chartName != "" || chartVersion != "" {
		env.Chart = &chartEnvelope{
			Metadata: &chartMetadata{Name: chartName, Version: chartVersion},
		}
	}

	data, err := encodeRelease(env)
	if err != nil {
		t.Fatalf("failed to encode release: %v", err)
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1." + releaseName + ".v1",
			Namespace: namespace,
			Labels: map[string]string{
				"owner":   "helm",
				"name":    releaseName,
				"status":  "deployed",
				"version": "1",
			},
		},
		Type: corev1.SecretType(helmSecretType),
		Data: map[string][]byte{
			"release": data,
		},
	}
}

func defaultSourceRef() helmv2.CrossNamespaceObjectReference {
	return helmv2.CrossNamespaceObjectReference{
		Kind:      "HelmRepository",
		Name:      "myrepo",
		Namespace: "flux-system",
	}
}

func TestConvertToHelmRelease_HappyPath(t *testing.T) {
	config := map[string]interface{}{
		"replicaCount": float64(3),
		"image": map[string]interface{}{
			"repository": "nginx",
			"tag":        "1.25.0",
		},
	}

	secret := validHelmSecret(t, "myapp", "production", "mychart", "1.2.3", config)
	result, err := ConvertToHelmRelease(secret, defaultSourceRef())
	if err != nil {
		t.Fatalf("ConvertToHelmRelease() error = %v", err)
	}

	if result.HelmRelease == nil {
		t.Fatal("result.HelmRelease is nil")
	}
	hr := result.HelmRelease

	if hr.APIVersion != helmv2.GroupVersion.String() {
		t.Errorf("APIVersion = %q, want %q", hr.APIVersion, helmv2.GroupVersion.String())
	}
	if hr.Kind != helmv2.HelmReleaseKind {
		t.Errorf("Kind = %q, want %q", hr.Kind, helmv2.HelmReleaseKind)
	}

	if hr.Name != "myapp" {
		t.Errorf("metadata.name = %q, want %q", hr.Name, "myapp")
	}
	if hr.Namespace != "production" {
		t.Errorf("metadata.namespace = %q, want %q", hr.Namespace, "production")
	}

	if hr.Spec.ReleaseName != "myapp" {
		t.Errorf("spec.releaseName = %q, want %q", hr.Spec.ReleaseName, "myapp")
	}
	if hr.Spec.TargetNamespace != "production" {
		t.Errorf("spec.targetNamespace = %q, want %q", hr.Spec.TargetNamespace, "production")
	}
	if hr.Spec.Interval.Duration != DefaultInterval {
		t.Errorf("spec.interval = %s, want %s", hr.Spec.Interval.Duration, DefaultInterval)
	}

	if hr.Spec.Chart == nil {
		t.Fatal("spec.chart is nil")
	}
	if hr.Spec.Chart.Spec.Chart != "mychart" {
		t.Errorf("spec.chart.spec.chart = %q, want %q", hr.Spec.Chart.Spec.Chart, "mychart")
	}
	if hr.Spec.Chart.Spec.Version != "1.2.3" {
		t.Errorf("spec.chart.spec.version = %q, want %q", hr.Spec.Chart.Spec.Version, "1.2.3")
	}
	if hr.Spec.Chart.Spec.SourceRef.Kind != "HelmRepository" {
		t.Errorf("spec.chart.spec.sourceRef.kind = %q, want %q", hr.Spec.Chart.Spec.SourceRef.Kind, "HelmRepository")
	}
	if hr.Spec.Chart.Spec.SourceRef.Name != "myrepo" {
		t.Errorf("spec.chart.spec.sourceRef.name = %q, want %q", hr.Spec.Chart.Spec.SourceRef.Name, "myrepo")
	}
	if hr.Spec.Chart.Spec.SourceRef.Namespace != "flux-system" {
		t.Errorf("spec.chart.spec.sourceRef.namespace = %q, want %q", hr.Spec.Chart.Spec.SourceRef.Namespace, "flux-system")
	}

	if hr.Spec.Values == nil {
		t.Fatal("spec.values is nil")
	}
	var got map[string]interface{}
	if err := json.Unmarshal(hr.Spec.Values.Raw, &got); err != nil {
		t.Fatalf("spec.values.Raw is not valid JSON: %v", err)
	}
	if got["replicaCount"] != float64(3) {
		t.Errorf("spec.values.replicaCount = %v, want 3", got["replicaCount"])
	}
	imageMap, ok := got["image"].(map[string]interface{})
	if !ok {
		t.Fatal("spec.values.image is not a map")
	}
	if imageMap["repository"] != "nginx" {
		t.Errorf("spec.values.image.repository = %v, want nginx", imageMap["repository"])
	}

	// The YAML body (header stripped) must round-trip back into a HelmRelease.
	var roundTrip helmv2.HelmRelease
	if err := yaml.Unmarshal([]byte(result.RawYAML), &roundTrip); err != nil {
		t.Fatalf("RawYAML did not parse: %v", err)
	}
	if roundTrip.Spec.ReleaseName != "myapp" {
		t.Errorf("round-tripped releaseName = %q, want %q", roundTrip.Spec.ReleaseName, "myapp")
	}
	if roundTrip.Spec.Chart.Spec.Chart != "mychart" {
		t.Errorf("round-tripped chart = %q, want %q", roundTrip.Spec.Chart.Spec.Chart, "mychart")
	}
}

func TestConvertToHelmRelease_EmptyValues(t *testing.T) {
	secret := validHelmSecret(t, "emptyapp", "default", "mychart", "1.0.0", nil)
	result, err := ConvertToHelmRelease(secret, defaultSourceRef())
	if err != nil {
		t.Fatalf("ConvertToHelmRelease() error = %v", err)
	}
	if result.HelmRelease.Spec.Values != nil {
		t.Errorf("spec.values should be nil for empty input, got %s", string(result.HelmRelease.Spec.Values.Raw))
	}
}

func TestConvertToHelmRelease_MissingChartMetadata(t *testing.T) {
	// Release with no chart block — chart name and version should stay empty.
	secret := validHelmSecret(t, "myapp", "production", "", "", nil)
	result, err := ConvertToHelmRelease(secret, defaultSourceRef())
	if err != nil {
		t.Fatalf("ConvertToHelmRelease() error = %v", err)
	}
	if result.HelmRelease.Spec.Chart.Spec.Chart != "" {
		t.Errorf("spec.chart.spec.chart should be empty, got %q", result.HelmRelease.Spec.Chart.Spec.Chart)
	}
	if result.HelmRelease.Spec.Chart.Spec.Version != "" {
		t.Errorf("spec.chart.spec.version should be empty, got %q", result.HelmRelease.Spec.Chart.Spec.Version)
	}
}

func TestConvertToHelmRelease_RejectsNonHelmSecret(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "not-a-helm-secret"},
		Type:       corev1.SecretTypeOpaque,
	}
	if _, err := ConvertToHelmRelease(secret, defaultSourceRef()); err == nil {
		t.Fatal("ConvertToHelmRelease() should return error for non-helm secret")
	}
}
