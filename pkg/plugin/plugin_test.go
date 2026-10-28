package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	log "github.com/sirupsen/logrus"
)

const canaryHash = "abc123"

func newMetric(t *testing.T, host, successCondition string) v1alpha1.Metric {
	t.Helper()
	cfg := Config{
		Host:         host,
		Metric:       "errors",
		Aggregation:  "MEAN",
		Granularity:  60,
		TimeWindowMs: 60000,
		Insecure:     true,
		TagFilters: []TagFilter{{
			Name:     "kubernetes.pod.label",
			Key:      "rollouts-pod-template-hash",
			Operator: "EQUALS",
			Value:    canaryHash,
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
			Plugin: map[string]json.RawMessage{PluginName: raw},
		},
	}
}

// mockInstana mimics the verified call-groups contract: one group per
// pod-template-hash, series keyed "<metric>.<agg lowercase>.<granularity>".
func mockInstana(t *testing.T, value float64) *httptest.Server {
	t.Helper()
	body := fmt.Sprintf(`{"totalHits":1,"items":[{"name":%q,"metrics":{"errors.mean.60":[[1783566960000,%v]]}}]}`, canaryHash, value)
	return mockInstanaBody(t, body)
}

// emptyCallGroups is what Instana returns when the tag filter matches no calls
// in the window, e.g. a call.http.status=500 filter on a healthy canary.
const emptyCallGroups = `{"totalHits":0,"items":[]}`

// mockInstanaBody serves a fixed call-groups response and asserts the request
// shape the plugin must send.
func mockInstanaBody(t *testing.T, response string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != MetricsPath {
			t.Errorf("plugin hit %q, want %q", r.URL.Path, MetricsPath)
		}
		if got := r.Header.Get("Authorization"); got != "apiToken test-token" {
			t.Errorf("unexpected auth header: %q", got)
		}

		var body callGroupsRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.TagFilterExpression == nil {
			t.Error("request had no tagFilterExpression; canary would not be scoped")
		} else if k := body.TagFilterExpression.Elements[0].Key; k != "rollouts-pod-template-hash" {
			t.Errorf("tag filter key = %q, want rollouts-pod-template-hash", k)
		}
		if body.Pagination.Page < 1 || body.Pagination.PageSize < 1 {
			t.Errorf("pagination must be >= 1, got %+v", body.Pagination)
		}
		if body.TimeFrame.To == 0 {
			t.Error("timeFrame.to must be a real timestamp; Instana treats 0 literally")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
}

func TestRun_HealthyCanaryPasses(t *testing.T) {
	t.Setenv(defaultAPITokenEnv, "test-token")
	srv := mockInstana(t, 1.5)
	defer srv.Close()

	p := &RpcPlugin{LogCtx: log.WithField("test", t.Name())}
	m := p.Run(&v1alpha1.AnalysisRun{}, newMetric(t, srv.URL, "result <= 5.0"))

	if m.Phase != v1alpha1.AnalysisPhaseSuccessful {
		t.Fatalf("expected Successful, got %s (value=%s, msg=%s)", m.Phase, m.Value, m.Message)
	}
	if m.Value != "1.5" {
		t.Errorf("expected value 1.5, got %s", m.Value)
	}
}

func TestRun_DegradedCanaryDoesNotPass(t *testing.T) {
	t.Setenv(defaultAPITokenEnv, "test-token")
	srv := mockInstana(t, 42)
	defer srv.Close()

	p := &RpcPlugin{LogCtx: log.WithField("test", t.Name())}
	m := p.Run(&v1alpha1.AnalysisRun{}, newMetric(t, srv.URL, "result <= 5.0"))

	if m.Phase == v1alpha1.AnalysisPhaseSuccessful {
		t.Fatalf("expected a non-Successful phase for a degraded canary, got Successful")
	}
}

func TestRun_MissingTokenErrors(t *testing.T) {
	p := &RpcPlugin{LogCtx: log.WithField("test", t.Name())}
	m := p.Run(&v1alpha1.AnalysisRun{}, newMetric(t, "https://example.instana.io", "result <= 5.0"))

	if m.Phase != v1alpha1.AnalysisPhaseError {
		t.Fatalf("expected Error phase when token missing, got %s", m.Phase)
	}
}

// An empty result is reported as nil, the same way the Datadog provider does
// it. A condition that does not opt in with default() must still error, so a
// canary emitting no telemetry cannot sail through an error-rate gate.
func TestRun_EmptyResultWithoutDefaultIsError(t *testing.T) {
	t.Setenv(defaultAPITokenEnv, "test-token")
	srv := mockInstanaBody(t, emptyCallGroups)
	defer srv.Close()

	p := &RpcPlugin{LogCtx: log.WithField("test", t.Name())}
	m := p.Run(&v1alpha1.AnalysisRun{}, newMetric(t, srv.URL, "result <= 5.0"))

	if m.Phase != v1alpha1.AnalysisPhaseError {
		t.Fatalf("expected Error without default(), got %s (value=%q)", m.Phase, m.Value)
	}
	if m.Value != "[]" {
		t.Errorf("expected value [] for an empty result, got %q", m.Value)
	}
}

// With default() the template decides what empty means. For a filter that
// selects the failure itself (call.http.status=500), empty is the healthy case.
func TestRun_EmptyResultWithDefaultPasses(t *testing.T) {
	t.Setenv(defaultAPITokenEnv, "test-token")
	srv := mockInstanaBody(t, emptyCallGroups)
	defer srv.Close()

	p := &RpcPlugin{LogCtx: log.WithField("test", t.Name())}
	m := p.Run(&v1alpha1.AnalysisRun{}, newMetric(t, srv.URL, "default(result, 0) <= 5.0"))

	if m.Phase != v1alpha1.AnalysisPhaseSuccessful {
		t.Fatalf("expected Successful with default(result, 0), got %s (value=%q msg=%q)", m.Phase, m.Value, m.Message)
	}
	if m.Value != "[]" {
		t.Errorf("expected value [] for an empty result, got %q", m.Value)
	}
}

// The default value is really evaluated, not short-circuited to Successful.
func TestRun_EmptyResultWithDefaultCanFail(t *testing.T) {
	t.Setenv(defaultAPITokenEnv, "test-token")
	srv := mockInstanaBody(t, emptyCallGroups)
	defer srv.Close()

	p := &RpcPlugin{LogCtx: log.WithField("test", t.Name())}
	m := p.Run(&v1alpha1.AnalysisRun{}, newMetric(t, srv.URL, "default(result, 1) <= 0"))

	if m.Phase != v1alpha1.AnalysisPhaseFailed {
		t.Fatalf("expected Failed with default(result, 1) <= 0, got %s (value=%q msg=%q)", m.Phase, m.Value, m.Message)
	}
}

// Groups came back but none is the selected one: the canary has no matching
// calls while stable does. That is empty, not ambiguous.
func TestRun_SelectGroupAbsentIsEmpty(t *testing.T) {
	t.Setenv(defaultAPITokenEnv, "test-token")
	srv := mockInstanaBody(t, `{"totalHits":1,"items":[{"name":"stablehash","metrics":{"errors.mean.60":[[1783566960000,0.9]]}}]}`)
	defer srv.Close()

	metric := newMetric(t, srv.URL, "default(result, 0) <= 5.0")
	var cfg Config
	if err := json.Unmarshal(metric.Provider.Plugin[PluginName], &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	cfg.SelectGroup = canaryHash
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	metric.Provider.Plugin[PluginName] = raw

	p := &RpcPlugin{LogCtx: log.WithField("test", t.Name())}
	m := p.Run(&v1alpha1.AnalysisRun{}, metric)

	if m.Phase != v1alpha1.AnalysisPhaseSuccessful {
		t.Fatalf("expected Successful, got %s (value=%q msg=%q)", m.Phase, m.Value, m.Message)
	}
	if m.Value != "[]" {
		t.Errorf("expected value [], got %q (stable's value must not leak into the canary gate)", m.Value)
	}
}

func TestParseConfig_Defaults(t *testing.T) {
	m := newMetric(t, "https://example.instana.io", "result <= 5.0")
	cfg, err := parseConfig(m)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.GroupByTag != defaultGroupByTag || cfg.GroupByTagKey != defaultGroupByTagKey {
		t.Errorf("grouping defaults not applied: %+v", cfg)
	}
	if cfg.PageSize != defaultPageSize || cfg.APITokenEnv != defaultAPITokenEnv {
		t.Errorf("defaults not applied: %+v", cfg)
	}
}

func TestMetricKey_MatchesInstanaSeriesNaming(t *testing.T) {
	cfg := &Config{Metric: "latency", Aggregation: "P90", Granularity: 60}
	if got := metricKey(cfg); got != "latency.p90.60" {
		t.Fatalf("metricKey = %q, want latency.p90.60", got)
	}
}

// An empty result is nil, never a healthy zero. The template decides via
// default(); without it, evaluation against nil errors (see the Run tests).
func TestReduceToScalar_EmptyResultIsNil(t *testing.T) {
	got, err := reduceToScalar(callGroupsResponse{}, "errors.mean.60", "")
	if err != nil {
		t.Fatalf("expected no error for an empty result set, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for an empty result set, got %v", *got)
	}
}

// A group that exists but carries no series for the key is a misconfiguration
// (wrong metric, aggregation, or granularity), not an empty result. It must
// stay an error so default() cannot turn a typo into a passing gate.
func TestReduceToScalar_MissingSeriesIsError(t *testing.T) {
	resp := callGroupsResponse{TotalHits: 1}
	resp.Items = append(resp.Items, struct {
		Name    string                 `json:"name"`
		Metrics map[string][][]float64 `json:"metrics"`
	}{Name: canaryHash, Metrics: map[string][][]float64{"latency.p90.60": {{1, 36}}}})

	if _, err := reduceToScalar(resp, "errors.mean.60", canaryHash); err == nil {
		t.Fatal("expected an error when the group has no series for the requested key")
	}
}

func TestReduceToScalar_SelectsNamedGroup(t *testing.T) {
	resp := callGroupsResponse{TotalHits: 2}
	resp.Items = append(resp.Items,
		struct {
			Name    string                 `json:"name"`
			Metrics map[string][][]float64 `json:"metrics"`
		}{Name: "stablehash", Metrics: map[string][][]float64{"errors.mean.60": {{1, 0.1}}}},
		struct {
			Name    string                 `json:"name"`
			Metrics map[string][][]float64 `json:"metrics"`
		}{Name: canaryHash, Metrics: map[string][][]float64{"errors.mean.60": {{1, 9.9}}}},
	)

	got, err := reduceToScalar(resp, "errors.mean.60", canaryHash)
	if err != nil {
		t.Fatalf("reduceToScalar: %v", err)
	}
	if got == nil || *got != 9.9 {
		t.Fatalf("selected the wrong group: got %v, want 9.9 (the canary)", got)
	}
}

// Ambiguity must be an error, not a coin flip between canary and stable.
func TestReduceToScalar_MultipleGroupsWithoutSelectIsError(t *testing.T) {
	resp := callGroupsResponse{TotalHits: 2}
	resp.Items = append(resp.Items,
		struct {
			Name    string                 `json:"name"`
			Metrics map[string][][]float64 `json:"metrics"`
		}{Name: "a", Metrics: map[string][][]float64{"errors.mean.60": {{1, 1}}}},
		struct {
			Name    string                 `json:"name"`
			Metrics map[string][][]float64 `json:"metrics"`
		}{Name: "b", Metrics: map[string][][]float64{"errors.mean.60": {{1, 2}}}},
	)

	if _, err := reduceToScalar(resp, "errors.mean.60", ""); err == nil {
		t.Fatal("expected an error when several groups match and none is selected")
	}
}
