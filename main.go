package main

import (
	"github.com/argoproj/argo-rollouts/metricproviders/plugin/rpc"
	goPlugin "github.com/hashicorp/go-plugin"
	log "github.com/sirupsen/logrus"

	"github.com/instana/rollouts-metric-plugin/pkg/plugin"
)

// Populated at build time via -ldflags -X.
var (
	version = "dev"
	commit  = "none"
)

// handshakeConfig must match the values the Argo Rollouts controller expects
// for metric provider plugins. Do not change these.
var handshakeConfig = goPlugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "ARGO_ROLLOUTS_RPC_PLUGIN",
	MagicCookieValue: "metrics",
}

func main() {
	// go-plugin reserves stdout for the handshake; logrus writes to stderr.
	logCtx := log.WithFields(log.Fields{
		"plugin":  "instana-metrics",
		"version": version,
		"commit":  commit,
	})

	impl := &plugin.RpcPlugin{LogCtx: logCtx}

	pluginMap := map[string]goPlugin.Plugin{
		"RpcMetricProviderPlugin": &rpc.RpcMetricProviderPlugin{Impl: impl},
	}

	goPlugin.Serve(&goPlugin.ServeConfig{
		HandshakeConfig: handshakeConfig,
		Plugins:         pluginMap,
	})
}
