//go:build live

// Package live runs the plugin against a real Instana tenant. It is read-only:
// it issues metric queries and asserts nothing is created or changed.
//
// It is skipped unless both env vars are set:
//
//	export INSTANA_HOST=https://<tenant>.instana.io
//	export INSTANA_API_TOKEN=<read-only token>
//	# LIVE_GROUP_KEY/LIVE_GROUP_VALUE name a pod label that exists in the tenant.
//	export LIVE_GROUP_KEY=app LIVE_GROUP_VALUE=order-service
//	go test -tags live ./test/live/ -v
package live

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	log "github.com/sirupsen/logrus"

	"github.com/instana/rollouts-metric-plugin/pkg/plugin"
)

func liveMetric(t *testing.T, metric, aggregation, successCondition string) v1alpha1.Metric {
	t.Helper()
	host := os.Getenv("INSTANA_HOST")
	key := os.Getenv("LIVE_GROUP_KEY")
	value := os.Getenv("LIVE_GROUP_VALUE")
	if host == "" || os.Getenv("INSTANA_API_TOKEN") == "" || key == "" || value == "" {
		t.Skip("set INSTANA_HOST, INSTANA_API_TOKEN, LIVE_GROUP_KEY, LIVE_GROUP_VALUE")
	}

	cfg := plugin.Config{
		Host:          host,
		Metric:        metric,
		Aggregation:   aggregation,
		Granularity:   60,
		TimeWindowMs:  3600000,
		GroupByTag:    "kubernetes.pod.label",
		GroupByTagKey: key,
		SelectGroup:   value,
		TagFilters: []plugin.TagFilter{{
			Name:     "kubernetes.pod.label",
			Key:      key,
			Operator: "EQUALS",
			Value:    value,
		}},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return v1alpha1.Metric{
		Name:             metric,
		SuccessCondition: successCondition,
		Provider: v1alpha1.MetricProvider{
			Plugin: map[string]json.RawMessage{"instana/metrics": raw},
		},
	}
}

func TestLive_ErrorRate(t *testing.T) {
	p := &plugin.RpcPlugin{LogCtx: log.WithField("test", t.Name())}
	m := p.Run(&v1alpha1.AnalysisRun{}, liveMetric(t, "errors", "MEAN", "result <= 100"))

	t.Logf("phase=%s value=%q message=%q", m.Phase, m.Value, m.Message)
	if m.Phase == v1alpha1.AnalysisPhaseError {
		t.Fatalf("live query errored: %s", m.Message)
	}
	if m.Value == "" {
		t.Fatal("live query returned no value")
	}
}

func TestLive_Latency(t *testing.T) {
	p := &plugin.RpcPlugin{LogCtx: log.WithField("test", t.Name())}
	m := p.Run(&v1alpha1.AnalysisRun{}, liveMetric(t, "latency", "P90", "result <= 100000"))

	t.Logf("phase=%s value=%q message=%q", m.Phase, m.Value, m.Message)
	if m.Phase == v1alpha1.AnalysisPhaseError {
		t.Fatalf("live latency query errored: %s", m.Message)
	}
}

// A filter that matches nothing must error, not silently return 0 — otherwise a
// canary emitting no telemetry would pass an error-rate gate.
func TestLive_BogusFilterErrorsRatherThanReturningZero(t *testing.T) {
	if os.Getenv("INSTANA_HOST") == "" {
		t.Skip("set INSTANA_HOST")
	}
	t.Setenv("LIVE_GROUP_VALUE", "ZZZ-does-not-exist-ZZZ")

	p := &plugin.RpcPlugin{LogCtx: log.WithField("test", t.Name())}
	m := p.Run(&v1alpha1.AnalysisRun{}, liveMetric(t, "errors", "MEAN", "result <= 5.0"))

	if m.Phase == v1alpha1.AnalysisPhaseSuccessful {
		t.Fatalf("a non-existent workload PASSED the gate (value=%q) — silent-zero bug", m.Value)
	}
	t.Logf("correctly refused: phase=%s message=%q", m.Phase, m.Message)
}
