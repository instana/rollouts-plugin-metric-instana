//go:build integration

// Package integration drives the compiled plugin binary the same way the Argo
// Rollouts controller does: as a subprocess over go-plugin RPC. It exercises
// the handshake, gob serialization of AnalysisRun/Metric/Measurement, and the
// Run() round trip. Instana itself is stubbed, so this needs no tenant.
//
//	make build && go test -tags integration ./test/integration/ -v
package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/argoproj/argo-rollouts/metricproviders/plugin/rpc"
	"github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	"github.com/hashicorp/go-hclog"
	goPlugin "github.com/hashicorp/go-plugin"
)

// Must match main.go and the controller's expectations.
var handshakeConfig = goPlugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "ARGO_ROLLOUTS_RPC_PLUGIN",
	MagicCookieValue: "metrics",
}

const pluginKey = "RpcMetricProviderPlugin"

func binaryPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("../../dist/rollouts-metric-plugin-instana")
	if err != nil {
		t.Fatalf("resolve binary path: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("plugin binary not built (run `make build`): %v", err)
	}
	return path
}

// stubInstana mimics the verified call-groups contract and asserts the plugin
// sent the canary tag filter and auth header.
func stubInstana(t *testing.T, errorRate float64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/application-monitoring/analyze/call-groups" {
			t.Errorf("plugin hit unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "apiToken integration-token" {
			t.Errorf("bad auth header: %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if _, ok := body["tagFilterExpression"]; !ok {
			t.Errorf("expected tagFilterExpression scoping the canary, got: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"totalHits":1,"items":[{"name":"canaryhash","metrics":{"errors.mean.60":[[1783566960000,%v]]}}]}`, errorRate)
	}))
}

// dispense launches the real binary and returns an RPC client to it.
func dispense(t *testing.T) (rpc.MetricProviderPlugin, func()) {
	t.Helper()

	cmd := exec.Command(binaryPath(t))
	cmd.Env = append(os.Environ(), "INSTANA_API_TOKEN=integration-token")

	client := goPlugin.NewClient(&goPlugin.ClientConfig{
		HandshakeConfig: handshakeConfig,
		Plugins:         map[string]goPlugin.Plugin{pluginKey: &rpc.RpcMetricProviderPlugin{}},
		Cmd:             cmd,
		Managed:         true,
		Logger:          hclog.New(&hclog.LoggerOptions{Level: hclog.Error}),
	})

	rpcClient, err := client.Client()
	if err != nil {
		client.Kill()
		t.Fatalf("plugin handshake failed: %v", err)
	}
	raw, err := rpcClient.Dispense(pluginKey)
	if err != nil {
		client.Kill()
		t.Fatalf("dispense %q: %v", pluginKey, err)
	}
	return raw.(rpc.MetricProviderPlugin), client.Kill
}

func canaryMetric(t *testing.T, host, successCondition string) v1alpha1.Metric {
	t.Helper()
	cfg := map[string]any{
		"host":         host,
		"metric":       "errors",
		"aggregation":  "MEAN",
		"granularity":  60,
		"timeWindowMs": 60000,
		"tagFilters": []map[string]string{{
			"name":     "kubernetes.pod.label",
			"key":      "rollouts-pod-template-hash",
			"operator": "EQUALS",
			"value":    "canaryhash",
		}},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return v1alpha1.Metric{
		Name:             "error-rate",
		SuccessCondition: successCondition,
		Provider: v1alpha1.MetricProvider{
			Plugin: map[string]json.RawMessage{"instana/metrics": raw},
		},
	}
}

func TestRPC_Handshake_And_Type(t *testing.T) {
	p, kill := dispense(t)
	defer kill()

	if got := p.Type(); got != "Instana" {
		t.Fatalf("Type() over RPC = %q, want %q", got, "Instana")
	}
}

func TestRPC_HealthyCanary_Promotes(t *testing.T) {
	srv := stubInstana(t, 1.2)
	defer srv.Close()

	p, kill := dispense(t)
	defer kill()

	m := p.Run(&v1alpha1.AnalysisRun{}, canaryMetric(t, srv.URL, "result <= 5.0"))
	if m.Phase != v1alpha1.AnalysisPhaseSuccessful {
		t.Fatalf("phase = %s (value=%q msg=%q), want Successful", m.Phase, m.Value, m.Message)
	}
	if m.Value != "1.2" {
		t.Errorf("value = %q, want 1.2", m.Value)
	}
}

func TestRPC_DegradedCanary_DoesNotPromote(t *testing.T) {
	srv := stubInstana(t, 37.5)
	defer srv.Close()

	p, kill := dispense(t)
	defer kill()

	m := p.Run(&v1alpha1.AnalysisRun{}, canaryMetric(t, srv.URL, "result <= 5.0"))
	if m.Phase == v1alpha1.AnalysisPhaseSuccessful {
		t.Fatalf("degraded canary was promoted; phase = %s value = %q", m.Phase, m.Value)
	}
	t.Logf("degraded canary correctly blocked: phase=%s value=%s", m.Phase, m.Value)
}

func TestRPC_InstanaUnreachable_Errors(t *testing.T) {
	p, kill := dispense(t)
	defer kill()

	// Port 1 is reserved and refuses connections.
	m := p.Run(&v1alpha1.AnalysisRun{}, canaryMetric(t, "http://127.0.0.1:1", "result <= 5.0"))
	if m.Phase != v1alpha1.AnalysisPhaseError {
		t.Fatalf("phase = %s, want Error when Instana is unreachable", m.Phase)
	}
}
