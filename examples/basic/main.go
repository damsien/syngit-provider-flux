package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	fluxprovider "github.com/syngit-org/syngit-provider-flux/pkg"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func main() {
	// Build a fake Helm release secret as it would appear in a cluster.
	// In production, you would obtain this from the Kubernetes API instead.
	secret := buildFakeHelmSecret()

	// The chart sourceRef points at the Flux source (HelmRepository,
	// GitRepository, or Bucket) providing the chart. It is not stored in
	// the Helm release secret, so the caller must supply it.
	sourceRef := helmv2.CrossNamespaceObjectReference{
		Kind:      "HelmRepository",
		Name:      "myrepo",
		Namespace: "flux-system",
	}

	result, err := fluxprovider.ConvertToHelmRelease(secret, sourceRef)
	if err != nil {
		log.Fatalf("failed to convert helm secret: %v", err)
	}

	hr := result.HelmRelease
	fmt.Printf("HelmRelease %s/%s:\n%s", hr.Namespace, hr.Name, result.RawYAML)
}

// buildFakeHelmSecret creates a corev1.Secret that mimics what Helm 3 stores
// in a cluster after "helm install myapp ./mychart -n production --set replicaCount=3".
func buildFakeHelmSecret() *corev1.Secret {
	release := map[string]interface{}{
		"name":      "myapp",
		"namespace": "production",
		"chart": map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":    "mychart",
				"version": "1.2.3",
			},
		},
		"config": map[string]interface{}{
			"replicaCount": 3,
			"image": map[string]interface{}{
				"repository": "nginx",
				"tag":        "1.25.0",
			},
			"service": map[string]interface{}{
				"type": "ClusterIP",
				"port": 8080,
			},
		},
	}

	// Encode: JSON -> gzip -> base64  (what Helm does before storing the secret)
	jsonData, _ := json.Marshal(release)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write(jsonData) // nolint:errcheck
	gz.Close()         // nolint:errcheck

	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.myapp.v1",
			Namespace: "production",
			Labels: map[string]string{
				"owner":   "helm",
				"name":    "myapp",
				"status":  "deployed",
				"version": "1",
			},
		},
		Type: corev1.SecretType("helm.sh/release.v1"),
		Data: map[string][]byte{
			"release": []byte(encoded),
		},
	}
}
