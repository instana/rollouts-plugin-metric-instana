package plugin

import goPlugin "github.com/hashicorp/go-plugin"

// HandshakeConfig must match the Argo Rollouts controller exactly, or the
// controller rejects the binary with "Failed to read any lines from plugin's
// stdout" and every measurement comes back Error.
//
// The authoritative values live in the controller's metric-plugin client
// (argo-rollouts metricproviders/plugin/client/client.go). Note the cookie
// value is "metricprovider", NOT "metrics" — the traffic-router plugin uses
// "trafficrouter" under the same cookie key.
//
// Both main.go and the integration test read these, so there is one definition
// to keep in step with upstream. A same-module test cannot catch drift from
// upstream on its own; the end-to-end demo is what actually proves it.
var HandshakeConfig = goPlugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "ARGO_ROLLOUTS_RPC_PLUGIN",
	MagicCookieValue: "metricprovider",
}

// PluginKey is the name the controller dispenses.
const PluginKey = "RpcMetricProviderPlugin"
