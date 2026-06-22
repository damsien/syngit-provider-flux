package fluxprovider

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
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

func jsonValues(t *testing.T, m map[string]interface{}) *apiextensionsv1.JSON {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("failed to marshal values: %v", err)
	}
	return &apiextensionsv1.JSON{Raw: raw}
}

// existingHelmRelease builds a HelmRelease with hand-tuned, non-default spec
// fields so tests can assert that ConvertToHelmReleaseWithExisting preserves
// everything except spec.values.
func existingHelmRelease(values *apiextensionsv1.JSON) *helmv2.HelmRelease {
	return &helmv2.HelmRelease{
		TypeMeta: metav1.TypeMeta{
			APIVersion: helmv2.GroupVersion.String(),
			Kind:       helmv2.HelmReleaseKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "existing-app",
			Namespace: "existing-ns",
		},
		Spec: helmv2.HelmReleaseSpec{
			Interval:        metav1.Duration{Duration: 30 * time.Minute},
			ReleaseName:     "existing-app",
			TargetNamespace: "existing-ns",
			Chart: &helmv2.HelmChartTemplate{
				Spec: helmv2.HelmChartTemplateSpec{
					Chart:   "existing-chart",
					Version: "9.9.9",
					SourceRef: helmv2.CrossNamespaceObjectReference{
						Kind:      "GitRepository",
						Name:      "existing-repo",
						Namespace: "existing-flux",
					},
				},
			},
			Values: values,
		},
	}
}

func TestConvertToHelmReleaseWithExisting_ReplacesValuesOnly(t *testing.T) {
	existing := existingHelmRelease(jsonValues(t, map[string]interface{}{"old": "value"}))

	newConfig := map[string]interface{}{
		"replicaCount": float64(5),
		"image":        map[string]interface{}{"repository": "redis", "tag": "7.0"},
	}
	// Secret name/namespace/chart name deliberately differ from existing to
	// prove they are ignored — only the values and chart version are taken
	// from the secret.
	secret := validHelmSecret(t, "secret-app", "secret-ns", "secret-chart", "0.0.1", newConfig)

	result, err := ConvertToHelmReleaseWithExisting(secret, existing)
	if err != nil {
		t.Fatalf("ConvertToHelmReleaseWithExisting() error = %v", err)
	}
	hr := result.HelmRelease

	// Values come from the secret and fully replace the old ones.
	if hr.Spec.Values == nil {
		t.Fatal("spec.values is nil")
	}
	var got map[string]interface{}
	if err := json.Unmarshal(hr.Spec.Values.Raw, &got); err != nil {
		t.Fatalf("spec.values.Raw is not valid JSON: %v", err)
	}
	if got["replicaCount"] != float64(5) {
		t.Errorf("spec.values.replicaCount = %v, want 5", got["replicaCount"])
	}
	if _, ok := got["old"]; ok {
		t.Errorf("spec.values still contains old key: %v", got)
	}

	// Everything else is preserved from the existing HelmRelease.
	if hr.Name != "existing-app" {
		t.Errorf("metadata.name = %q, want %q", hr.Name, "existing-app")
	}
	if hr.Namespace != "existing-ns" {
		t.Errorf("metadata.namespace = %q, want %q", hr.Namespace, "existing-ns")
	}
	if hr.Spec.ReleaseName != "existing-app" {
		t.Errorf("spec.releaseName = %q, want %q", hr.Spec.ReleaseName, "existing-app")
	}
	if hr.Spec.TargetNamespace != "existing-ns" {
		t.Errorf("spec.targetNamespace = %q, want %q", hr.Spec.TargetNamespace, "existing-ns")
	}
	if hr.Spec.Interval.Duration != 30*time.Minute {
		t.Errorf("spec.interval = %s, want %s", hr.Spec.Interval.Duration, 30*time.Minute)
	}
	if hr.Spec.Chart.Spec.Chart != "existing-chart" {
		t.Errorf("spec.chart.spec.chart = %q, want %q", hr.Spec.Chart.Spec.Chart, "existing-chart")
	}
	// The chart version is updated to match the secret's release.
	if hr.Spec.Chart.Spec.Version != "0.0.1" {
		t.Errorf("spec.chart.spec.version = %q, want %q", hr.Spec.Chart.Spec.Version, "0.0.1")
	}
	if hr.Spec.Chart.Spec.SourceRef.Kind != "GitRepository" {
		t.Errorf("spec.chart.spec.sourceRef.kind = %q, want %q", hr.Spec.Chart.Spec.SourceRef.Kind, "GitRepository")
	}
	if hr.Spec.Chart.Spec.SourceRef.Name != "existing-repo" {
		t.Errorf("spec.chart.spec.sourceRef.name = %q, want %q", hr.Spec.Chart.Spec.SourceRef.Name, "existing-repo")
	}

	// The YAML body must round-trip back into a HelmRelease.
	var roundTrip helmv2.HelmRelease
	if err := yaml.Unmarshal([]byte(result.RawYAML), &roundTrip); err != nil {
		t.Fatalf("RawYAML did not parse: %v", err)
	}
	if roundTrip.Spec.Chart.Spec.Chart != "existing-chart" {
		t.Errorf("round-tripped chart = %q, want %q", roundTrip.Spec.Chart.Spec.Chart, "existing-chart")
	}
}

func TestConvertToHelmReleaseWithExisting_UpdatesChartVersion(t *testing.T) {
	// existingHelmRelease pins version "9.9.9"; the secret pins "1.2.3".
	existing := existingHelmRelease(jsonValues(t, map[string]interface{}{"old": "value"}))
	secret := validHelmSecret(t, "secret-app", "secret-ns", "secret-chart", "1.2.3", nil)

	result, err := ConvertToHelmReleaseWithExisting(secret, existing)
	if err != nil {
		t.Fatalf("ConvertToHelmReleaseWithExisting() error = %v", err)
	}

	if got := result.HelmRelease.Spec.Chart.Spec.Version; got != "1.2.3" {
		t.Errorf("spec.chart.spec.version = %q, want %q", got, "1.2.3")
	}
	// Chart name and sourceRef are still preserved from the existing release.
	if got := result.HelmRelease.Spec.Chart.Spec.Chart; got != "existing-chart" {
		t.Errorf("spec.chart.spec.chart = %q, want %q", got, "existing-chart")
	}
}

func TestConvertToHelmReleaseWithExisting_DoesNotMutateInput(t *testing.T) {
	existing := existingHelmRelease(jsonValues(t, map[string]interface{}{"old": "value"}))
	before := existing.DeepCopy()

	secret := validHelmSecret(t, "secret-app", "secret-ns", "secret-chart", "0.0.1",
		map[string]interface{}{"new": "value"})

	if _, err := ConvertToHelmReleaseWithExisting(secret, existing); err != nil {
		t.Fatalf("ConvertToHelmReleaseWithExisting() error = %v", err)
	}

	if !reflect.DeepEqual(existing, before) {
		t.Errorf("input HelmRelease was mutated:\n before = %+v\n after  = %+v", before, existing)
	}
}

func TestConvertToHelmReleaseWithExisting_EmptyValuesClears(t *testing.T) {
	existing := existingHelmRelease(jsonValues(t, map[string]interface{}{"old": "value"}))
	secret := validHelmSecret(t, "secret-app", "secret-ns", "secret-chart", "0.0.1", nil)

	result, err := ConvertToHelmReleaseWithExisting(secret, existing)
	if err != nil {
		t.Fatalf("ConvertToHelmReleaseWithExisting() error = %v", err)
	}
	if result.HelmRelease.Spec.Values != nil {
		t.Errorf("spec.values should be nil for empty config, got %s", string(result.HelmRelease.Spec.Values.Raw))
	}
}

func TestConvertToHelmReleaseWithExisting_NilExisting(t *testing.T) {
	secret := validHelmSecret(t, "secret-app", "secret-ns", "secret-chart", "0.0.1", nil)
	if _, err := ConvertToHelmReleaseWithExisting(secret, nil); err == nil {
		t.Fatal("ConvertToHelmReleaseWithExisting() should return error for nil existing")
	}
}

func TestConvertToHelmReleaseWithExisting_RejectsNonHelmSecret(t *testing.T) {
	existing := existingHelmRelease(nil)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "not-a-helm-secret"},
		Type:       corev1.SecretTypeOpaque,
	}
	if _, err := ConvertToHelmReleaseWithExisting(secret, existing); err == nil {
		t.Fatal("ConvertToHelmReleaseWithExisting() should return error for non-helm secret")
	}
}

func TestIsCorrectHelmRelease(t *testing.T) {
	tests := []struct {
		name      string
		object    map[string]interface{}
		matchName string
		matchNS   string
		want      bool
	}{
		{
			name: "name and namespace match",
			object: map[string]interface{}{
				"apiVersion": fluxHelmGroup + "/v2",
				"kind":       helmReleaseKind,
				"metadata": map[string]interface{}{
					"name":      "myapp",
					"namespace": "production",
				},
			},
			matchName: "myapp",
			matchNS:   "production",
			want:      true,
		},
		{
			name: "name matches, manifest has no namespace",
			object: map[string]interface{}{
				"apiVersion": fluxHelmGroup + "/v2",
				"kind":       helmReleaseKind,
				"metadata": map[string]interface{}{
					"name": "myapp",
				},
			},
			matchName: "myapp",
			matchNS:   "production",
			want:      false,
		},
		{
			name: "name matches, caller passes empty namespace",
			object: map[string]interface{}{
				"apiVersion": fluxHelmGroup + "/v2beta1",
				"kind":       helmReleaseKind,
				"metadata": map[string]interface{}{
					"name":      "myapp",
					"namespace": "production",
				},
			},
			matchName: "myapp",
			matchNS:   "",
			want:      false,
		},
		{
			name: "name matches, both have empty namespaces",
			object: map[string]interface{}{
				"apiVersion": fluxHelmGroup + "/v2beta1",
				"kind":       helmReleaseKind,
				"metadata": map[string]interface{}{
					"name": "myapp",
				},
			},
			matchName: "myapp",
			matchNS:   "",
			want:      true,
		},
		{
			name: "namespace mismatch",
			object: map[string]interface{}{
				"apiVersion": fluxHelmGroup + "/v2",
				"kind":       helmReleaseKind,
				"metadata": map[string]interface{}{
					"name":      "myapp",
					"namespace": "staging",
				},
			},
			matchName: "myapp",
			matchNS:   "production",
			want:      false,
		},
		{
			name: "name mismatch",
			object: map[string]interface{}{
				"apiVersion": fluxHelmGroup + "/v2",
				"kind":       helmReleaseKind,
				"metadata": map[string]interface{}{
					"name":      "otherapp",
					"namespace": "production",
				},
			},
			matchName: "myapp",
			matchNS:   "production",
			want:      false,
		},
		{
			name: "wrong kind",
			object: map[string]interface{}{
				"apiVersion": fluxHelmGroup + "/v2",
				"kind":       "Kustomization",
				"metadata": map[string]interface{}{
					"name": "myapp",
				},
			},
			matchName: "myapp",
			matchNS:   "production",
			want:      false,
		},
		{
			name: "wrong api group",
			object: map[string]interface{}{
				"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
				"kind":       helmReleaseKind,
				"metadata": map[string]interface{}{
					"name": "myapp",
				},
			},
			matchName: "myapp",
			matchNS:   "production",
			want:      false,
		},
		{
			name: "group as exact apiVersion without version suffix",
			object: map[string]interface{}{
				"apiVersion": fluxHelmGroup,
				"kind":       helmReleaseKind,
				"metadata": map[string]interface{}{
					"name": "myapp",
				},
			},
			matchName: "myapp",
			matchNS:   "production",
			want:      false,
		},
		{
			name:      "missing metadata",
			object:    map[string]interface{}{"apiVersion": fluxHelmGroup + "/v2", "kind": helmReleaseKind},
			matchName: "myapp",
			matchNS:   "production",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCorrectHelmRelease(tt.object, tt.matchName, tt.matchNS); got != tt.want {
				t.Errorf("IsCorrectHelmRelease() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSourceRefFromHelmRelease(t *testing.T) {
	helmReleaseWithSourceRef := func(sourceRef map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{
			"spec": map[string]interface{}{
				"chart": map[string]interface{}{
					"spec": map[string]interface{}{
						"sourceRef": sourceRef,
					},
				},
			},
		}
	}

	t.Run("full sourceRef", func(t *testing.T) {
		obj := helmReleaseWithSourceRef(map[string]interface{}{
			"apiVersion": "source.toolkit.fluxcd.io/v1",
			"kind":       "HelmRepository",
			"name":       "myrepo",
			"namespace":  "flux-system",
		})

		ref, ok := ExtractSourceRefFromHelmRelease(obj)
		if !ok {
			t.Fatal("SourceRefFromHelmRelease() ok = false, want true")
		}
		if ref.APIVersion != "source.toolkit.fluxcd.io/v1" {
			t.Errorf("apiVersion = %q, want %q", ref.APIVersion, "source.toolkit.fluxcd.io/v1")
		}
		if ref.Kind != "HelmRepository" {
			t.Errorf("kind = %q, want %q", ref.Kind, "HelmRepository")
		}
		if ref.Name != "myrepo" {
			t.Errorf("name = %q, want %q", ref.Name, "myrepo")
		}
		if ref.Namespace != "flux-system" {
			t.Errorf("namespace = %q, want %q", ref.Namespace, "flux-system")
		}
	})

	t.Run("name only", func(t *testing.T) {
		obj := helmReleaseWithSourceRef(map[string]interface{}{"name": "myrepo"})

		ref, ok := ExtractSourceRefFromHelmRelease(obj)
		if !ok {
			t.Fatal("SourceRefFromHelmRelease() ok = false, want true")
		}
		if ref.Name != "myrepo" {
			t.Errorf("name = %q, want %q", ref.Name, "myrepo")
		}
		if ref.Kind != "" || ref.APIVersion != "" || ref.Namespace != "" {
			t.Errorf("expected only name to be set, got %+v", ref)
		}
	})

	t.Run("sourceRef without name", func(t *testing.T) {
		obj := helmReleaseWithSourceRef(map[string]interface{}{
			"kind": "HelmRepository",
		})

		if _, ok := ExtractSourceRefFromHelmRelease(obj); ok {
			t.Error("SourceRefFromHelmRelease() ok = true, want false when name absent")
		}
	})

	t.Run("sourceRef absent", func(t *testing.T) {
		obj := map[string]interface{}{
			"spec": map[string]interface{}{
				"chart": map[string]interface{}{
					"spec": map[string]interface{}{},
				},
			},
		}

		if _, ok := ExtractSourceRefFromHelmRelease(obj); ok {
			t.Error("SourceRefFromHelmRelease() ok = true, want false when sourceRef absent")
		}
	})

	t.Run("empty object", func(t *testing.T) {
		if _, ok := ExtractSourceRefFromHelmRelease(map[string]interface{}{}); ok {
			t.Error("SourceRefFromHelmRelease() ok = true, want false for empty object")
		}
	})

	t.Run("malformed sourceRef is not a map", func(t *testing.T) {
		obj := map[string]interface{}{
			"spec": map[string]interface{}{
				"chart": map[string]interface{}{
					"spec": map[string]interface{}{
						"sourceRef": "not-a-map",
					},
				},
			},
		}

		if _, ok := ExtractSourceRefFromHelmRelease(obj); ok {
			t.Error("SourceRefFromHelmRelease() ok = true, want false for malformed sourceRef")
		}
	})
}
