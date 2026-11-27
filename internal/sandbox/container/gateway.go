package container

import (
	"context"
	"fmt"
	"maps"
	"memeval/internal/sandbox"
	"net"
	"net/url"
	"strings"
)

func gatewayPort(gw *url.URL) string {
	if p := gw.Port(); p != "" {
		return p
	}
	if gw.Scheme == "https" {
		return "443"
	}
	return "80"
}
func gatewayTarget(gw *url.URL) (string, error) {
	if gw == nil {
		return "", fmt.Errorf("gateway mode requires --sut-llm-base-url")
	}
	if gw.Scheme != "http" {
		return "", fmt.Errorf("gateway relay supports http:// only; use open network for https://")
	}
	if gw.User != nil || gw.RawQuery != "" || gw.Fragment != "" {
		return "", fmt.Errorf("gateway URL must not contain credentials, query or fragment")
	}
	target := net.JoinHostPort(gw.Hostname(), gatewayPort(gw))
	if err := literalEndpoint(target, false); err != nil {
		return "", fmt.Errorf("gateway target requires a literal IP:port; use open network for a hostname")
	}
	return target, nil
}

// GatewayBaseURL preserves the configured path while addressing the episode's
// relay. It also supports expansion inside composed manifest environment values.
func GatewayBaseURL(gw *url.URL) string {
	if gw == nil {
		return ""
	}
	u := *gw
	u.Scheme = "http"
	u.Host = net.JoinHostPort("llm-gateway", gatewayPort(gw))
	return u.String()
}

func rewriteGatewayURL(env map[string]string, gw *url.URL) map[string]string {
	out := maps.Clone(env)
	if gw == nil {
		return out
	}
	for k, v := range out {
		if v == gw.String() || v == "{llm.base_url}" {
			out[k] = GatewayBaseURL(gw)
		}
	}
	return out
}

func EgressAllowlist(mode sandbox.NetworkMode, gw *url.URL) []string {
	if mode == sandbox.NetworkNone {
		return []string{}
	}
	if mode == sandbox.NetworkGateway && gw != nil {
		return []string{net.JoinHostPort(gw.Hostname(), gatewayPort(gw))}
	}
	return nil
}

func (b *Backend) createAux(ctx context.Context, e *episode) error {
	args, err := b.runArgs(e.config)
	if err != nil {
		return err
	}
	raw, err := b.call(ctx, args...)
	if err != nil {
		return err
	}
	e.containerID = strings.TrimSpace(string(raw))
	v, err := b.inspectContainer(ctx, e.containerID)
	if err != nil {
		return err
	}
	return e.recordContainer(v)
}
