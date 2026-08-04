package plugin

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// MetricsPath is the Instana endpoint this plugin queries.
//
// Verified against a live tenant. Two things make this the right endpoint and
// /api/application-monitoring/metrics/services the wrong one:
//
//   - metrics/services silently DISCARDS tagFilterExpression. It returns 200
//     and the full unfiltered result set, so a canary gate built on it would
//     silently score cluster-wide traffic instead of the canary.
//   - call-groups honours tagFilterExpression and can group by KEY_VALUE_PAIR
//     tags (kubernetes.pod.label) via groupbyTagSecondLevelKey, which is how
//     the canary/stable split is expressed.
const MetricsPath = "/api/application-monitoring/analyze/call-groups"

type instanaClient struct {
	host  string
	token string
	http  *http.Client
	now   func() time.Time
}

func newInstanaClient(cfg *Config) (*instanaClient, error) {
	token := os.Getenv(cfg.APITokenEnv)
	if token == "" {
		return nil, fmt.Errorf("Instana API token not set; expected it in env var %q", cfg.APITokenEnv)
	}

	transport := &http.Transport{}
	if cfg.Insecure {
		// InsecureSkipVerify is intentional: this path is only reached when the
		// operator explicitly sets insecure:true in the AnalysisTemplate, which
		// is documented as a test-cluster-only escape hatch. Certificate chain
		// validation and hostname verification are both enforced by default
		// (cfg.Insecure == false). Setting InsecureSkipVerify disables both in
		// one flag — there is no separate hostname-verification field in Go's
		// tls.Config.
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // intentional opt-in, see comment above // NOSONAR S4830 S4423
	}

	return &instanaClient{
		host:  strings.TrimRight(cfg.Host, "/"),
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second, Transport: transport},
		now:   time.Now,
	}, nil
}

type metricSpec struct {
	Metric      string `json:"metric"`
	Aggregation string `json:"aggregation"`
	Granularity int    `json:"granularity"`
}

type timeFrame struct {
	To         int64 `json:"to"`         // epoch millis; 0 is taken literally, not "now"
	WindowSize int64 `json:"windowSize"` // millis
}

type groupSpec struct {
	GroupbyTag       string `json:"groupbyTag"`
	GroupbyTagEntity string `json:"groupbyTagEntity"`
	// Required for KEY_VALUE_PAIR tags such as kubernetes.pod.label: it names
	// the label key (e.g. rollouts-pod-template-hash).
	GroupbyTagSecondLevelKey string `json:"groupbyTagSecondLevelKey,omitempty"`
}

type pagination struct {
	Page     int `json:"page"`     // must be >= 1
	PageSize int `json:"pageSize"` // must be >= 1
}

type tagFilterLeaf struct {
	Type     string `json:"type"` // always "TAG_FILTER"
	Name     string `json:"name"`
	Operator string `json:"operator"`
	Entity   string `json:"entity,omitempty"`
	// Key is required for KEY_VALUE_PAIR tags and omitted for STRING tags.
	Key   string `json:"key,omitempty"`
	Value string `json:"value"`
}

type tagFilterExpression struct {
	Type            string          `json:"type"` // "EXPRESSION"
	LogicalOperator string          `json:"logicalOperator"`
	Elements        []tagFilterLeaf `json:"elements"`
}

type callGroupsRequest struct {
	Group               groupSpec            `json:"group"`
	Metrics             []metricSpec         `json:"metrics"`
	TimeFrame           timeFrame            `json:"timeFrame"`
	Pagination          pagination           `json:"pagination"`
	TagFilterExpression *tagFilterExpression `json:"tagFilterExpression,omitempty"`
}

type callGroupsResponse struct {
	TotalHits int `json:"totalHits"`
	Items     []struct {
		Name    string                 `json:"name"`
		Metrics map[string][][]float64 `json:"metrics"`
	} `json:"items"`
}

// metricKey mirrors how Instana names the returned series: the aggregation is
// lower-cased and the granularity is appended, e.g. "errors.mean.60".
func metricKey(cfg *Config) string {
	return fmt.Sprintf("%s.%s.%d", cfg.Metric, strings.ToLower(cfg.Aggregation), cfg.Granularity)
}

func buildTagFilterExpression(filters []TagFilter) *tagFilterExpression {
	if len(filters) == 0 {
		return nil
	}
	elements := make([]tagFilterLeaf, 0, len(filters))
	for _, f := range filters {
		op := f.Operator
		if op == "" {
			op = "EQUALS"
		}
		entity := f.Entity
		if entity == "" {
			entity = "DESTINATION"
		}
		elements = append(elements, tagFilterLeaf{
			Type:     "TAG_FILTER",
			Name:     f.Name,
			Operator: op,
			Entity:   entity,
			Key:      f.Key,
			Value:    f.Value,
		})
	}
	return &tagFilterExpression{
		Type:            "EXPRESSION",
		LogicalOperator: "AND",
		Elements:        elements,
	}
}

func (c *instanaClient) buildRequest(cfg *Config) callGroupsRequest {
	return callGroupsRequest{
		Group: groupSpec{
			GroupbyTag:               cfg.GroupByTag,
			GroupbyTagEntity:         cfg.GroupByTagEntity,
			GroupbyTagSecondLevelKey: cfg.GroupByTagKey,
		},
		Metrics: []metricSpec{{
			Metric:      cfg.Metric,
			Aggregation: cfg.Aggregation,
			Granularity: cfg.Granularity,
		}},
		TimeFrame: timeFrame{
			To:         c.now().UnixMilli(),
			WindowSize: cfg.TimeWindowMs,
		},
		Pagination:          pagination{Page: 1, PageSize: cfg.PageSize},
		TagFilterExpression: buildTagFilterExpression(effectiveTagFilters(cfg)),
	}
}

// getMetricValue runs the query and reduces the response to a single scalar.
func (c *instanaClient) getMetricValue(cfg *Config) (float64, error) {
	payload, err := json.Marshal(c.buildRequest(cfg))
	if err != nil {
		return 0, fmt.Errorf("failed to encode request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.host+MetricsPath, bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "apiToken "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("Instana request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to read Instana response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("Instana returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed callGroupsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, fmt.Errorf("failed to parse Instana response: %w", err)
	}
	return reduceToScalar(parsed, metricKey(cfg), cfg.SelectGroup)
}

// reduceToScalar picks the group we care about and returns its latest value.
//
// An empty result is an error, never zero: for an error-rate gate, silently
// returning 0 would promote a canary that is emitting no telemetry at all.
func reduceToScalar(resp callGroupsResponse, key, selectGroup string) (float64, error) {
	if len(resp.Items) == 0 {
		return 0, fmt.Errorf("Instana returned no groups for metric %q (check tagFilters and that the workload is reporting)", key)
	}

	idx := 0
	if selectGroup != "" {
		idx = -1
		for i, item := range resp.Items {
			if item.Name == selectGroup {
				idx = i
				break
			}
		}
		if idx < 0 {
			return 0, fmt.Errorf("no group named %q in Instana response for metric %q", selectGroup, key)
		}
	} else if len(resp.Items) > 1 {
		return 0, fmt.Errorf("Instana returned %d groups for metric %q; narrow with tagFilters or set selectGroup", len(resp.Items), key)
	}

	series, ok := resp.Items[idx].Metrics[key]
	if !ok || len(series) == 0 {
		return 0, fmt.Errorf("no data points for metric %q in group %q", key, resp.Items[idx].Name)
	}

	last := series[len(series)-1]
	if len(last) < 2 {
		return 0, fmt.Errorf("malformed data point for metric %q", key)
	}
	// data point is [timestampMillis, value]
	return last[1], nil
}
