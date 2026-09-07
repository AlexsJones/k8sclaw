package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// Fake digest and endpoint are render fixtures only, never deployment evidence.
const cellnDeploymentTestSettings = "celln.enabled=true,celln.allowInsecureHttp=true,celln.tokenSecret=controller-client,celln.router.clientTokenSecret=router-client,celln.router.backendTokenSecret=dispatcher,celln.router.ownershipClaim=owners,celln.router.allowInsecureBackends=true,celln.router.backends[0]=http://node-a:8787,celln.router.backends[1]=http://node-b:8787,celln.router.image.digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa,celln.capabilityTokenSecret=api-discovery,celln.router.capabilityTokenSecret=router-discovery"

func TestCellnDeployment(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required for chart rendering")
	}
	for _, installer := range []string{"false", "true"} {
		out, err := exec.Command("helm", "template", "m0", "../../charts/sympozium", "--set", cellnDeploymentTestSettings, "--set", "celln.installer.enabled="+installer).CombinedOutput()
		if err != nil {
			t.Fatalf("render: %v\n%s", err, out)
		}
		decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(out), 4096)
		var router appsv1.Deployment
		var apiServer appsv1.Deployment
		foundInstaller := false
		for {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if len(bytes.TrimSpace(raw)) == 0 {
				continue
			}
			var meta struct {
				Kind     string
				Metadata struct{ Name string }
			}
			if err := json.Unmarshal(raw, &meta); err != nil {
				t.Fatal(err)
			}
			if meta.Metadata.Name == "celln-installer" && meta.Kind == "DaemonSet" {
				foundInstaller = true
			}
			if meta.Metadata.Name == "celln-router" && meta.Kind == "DaemonSet" {
				t.Fatal("per-node router still rendered")
			}
			if meta.Metadata.Name == "celln-router" && meta.Kind == "Deployment" {
				if err := json.Unmarshal(raw, &router); err != nil {
					t.Fatal(err)
				}
			}
			if meta.Metadata.Name == "sympozium-apiserver" && meta.Kind == "Deployment" {
				if err := json.Unmarshal(raw, &apiServer); err != nil {
					t.Fatal(err)
				}
			}
		}
		if foundInstaller != (installer == "true") {
			t.Fatal("host installation must be an independent opt-in")
		}
		if router.Spec.Replicas == nil || *router.Spec.Replicas != 2 {
			t.Fatal("missing replicated router")
		}
		pod := router.Spec.Template.Spec
		if pod.HostPID || pod.HostNetwork || pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
			t.Fatal("router has host/API authority")
		}
		if len(pod.Containers) != 1 {
			t.Fatal("unexpected router containers")
		}
		container := pod.Containers[0]
		if !strings.Contains(container.Image, "@sha256:") || len(container.Command) != 1 || container.Command[0] != "/usr/local/bin/celln" {
			t.Fatal("router must execute image-pinned binary directly")
		}
		args := strings.Join(container.Args, " ")
		for _, want := range []string{"--backends http://node-a:8787,http://node-b:8787", "--client-token-file /etc/celln/client/token", "--token-file /etc/celln/backend/token", "--ownership-dir /var/lib/celln/ownership"} {
			if !strings.Contains(args, want) {
				t.Fatalf("missing %s", want)
			}
		}
		if len(pod.Volumes) != 4 {
			t.Fatal("unexpected router volumes")
		}
		for _, volume := range pod.Volumes {
			if volume.HostPath != nil || volume.EmptyDir != nil {
				t.Fatal("router must not use host binaries or ephemeral ownership")
			}
			switch volume.Name {
			case "ownership":
				if volume.PersistentVolumeClaim == nil || volume.PersistentVolumeClaim.ClaimName != "owners" {
					t.Fatal("shared ownership missing")
				}
			case "client-token", "backend-token", "capability-token":
				if volume.Secret == nil || volume.Secret.DefaultMode == nil || *volume.Secret.DefaultMode != 0440 {
					t.Fatal("bounded Secret permissions missing")
				}
			default:
				t.Fatal("unknown volume")
			}
		}
		for _, mount := range container.VolumeMounts {
			if mount.SubPath != "" {
				t.Fatal("subPath prevents credential rotation")
			}
		}
		if !strings.Contains(args, "--capability-token-file /etc/celln/capability/token") {
			t.Fatal("missing read-only router credential")
		}
		found := false
		for _, volume := range apiServer.Spec.Template.Spec.Volumes {
			if volume.Secret != nil && (volume.Secret.SecretName == "controller-client" || volume.Secret.SecretName == "dispatcher" || volume.Secret.SecretName == "router-client") {
				t.Fatal("API server received execution credential")
			}
			if volume.Name == "celln-capability-token" {
				found = volume.Secret != nil && volume.Secret.SecretName == "api-discovery"
			}
		}
		if !found {
			t.Fatal("missing API read-only credential")
		}
		for _, mount := range apiServer.Spec.Template.Spec.Containers[0].VolumeMounts {
			if mount.Name == "celln-capability-token" && (!mount.ReadOnly || mount.SubPath != "") {
				t.Fatal("discovery credential must rotate in read-only projected directory")
			}
		}
	}
}

func TestCellnDeploymentRefusesIncompleteConfiguration(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required for chart rendering")
	}
	for _, override := range []string{
		"celln.capabilityTokenSecret=", "celln.router.capabilityTokenSecret=", "celln.capabilityTokenSecret=controller-client", "celln.router.capabilityTokenSecret=dispatcher", "celln.router.capabilityTokenSecret=router-client",
		"celln.router.image.digest=", "celln.router.image.digest=latest",
		"celln.router.ownershipClaim=", "celln.router.clientTokenSecret=",
		"celln.router.backendTokenSecret=router-client", "celln.tokenSecret=",
		"celln.router.allowInsecureBackends=false", "celln.allowInsecureHttp=false",
		"celln.router.backends[0]=https://node-a:8787", "celln.router.backends[0]=http://user@node-a:8787",
	} {
		t.Run(override, func(t *testing.T) {
			if out, err := exec.Command("helm", "template", "m0", "../../charts/sympozium", "--set", cellnDeploymentTestSettings, "--set", override).CombinedOutput(); err == nil {
				t.Fatalf("accepted incomplete/unsafe configuration: %s", out)
			}
		})
	}
	out, err := exec.Command("helm", "template", "m0", "../../charts/sympozium").CombinedOutput()
	if err != nil {
		t.Fatalf("default chart: %v\n%s", err, out)
	}
	if bytes.Contains(out, []byte("name: celln-installer")) || bytes.Contains(out, []byte("name: celln-router")) {
		t.Fatal("default install must not deploy experimental Celln resources")
	}
}
