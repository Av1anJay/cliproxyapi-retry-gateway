package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func TestExecuteStreamRequiresStreamID(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodExecutorExecuteStream, []byte(`{}`))
	if err != nil {
		t.Fatalf("handleMethod returned error: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.OK || env.Error == nil || env.Error.Code != "executor_error" {
		t.Fatalf("envelope = %#v, want executor_error", env)
	}
}
