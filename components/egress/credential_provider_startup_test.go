package main

import (
	"context"
	"testing"

	"github.com/alibaba/opensandbox/egress/pkg/constants"
	"github.com/alibaba/opensandbox/egress/pkg/policy"
)

func TestPolicyStartupValidatesCredentialProviderConfiguration(t *testing.T) {
	t.Setenv(constants.EnvCredentialProviderConfig, `[{"name":"../invalid-provider","args":[]}]`)
	server, err := startPolicyServer(
		&stubProxy{updated: policy.DefaultDenyPolicy()}, nil, "dns", "127.0.0.1:0", "management",
		nil, "", nil, nil, nil,
	)
	if err == nil {
		_ = server.Shutdown(context.Background())
		t.Fatal("operator credential provider configuration was ignored")
	}
}
