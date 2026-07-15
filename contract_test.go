package unio

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/Fullstop000/unio/driver"
	"github.com/Fullstop000/unio/errs"
)

func TestFrozenValuesMatchContractManifest(t *testing.T) {
	data, err := os.ReadFile("docs/contract-v0.6.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		SpecVersion       string   `json:"spec_version"`
		AgentKind         []string `json:"agent_kind"`
		SessionState      []string `json:"session_state"`
		BlockedKind       []string `json:"blocked_kind"`
		EventKind         []string `json:"event_kind"`
		ErrorKind         []string `json:"error_kind"`
		SessionDataFormat []string `json:"session_data_format"`
		DriverTransport   []string `json:"driver_transport"`
		DriverLifecycle   []string `json:"driver_lifecycle"`
		DriverEventType   []string `json:"driver_event_type"`
		FinishReason      []string `json:"finish_reason"`
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	if contract.SpecVersion != "0.6.0" {
		t.Fatalf("unexpected spec version %q", contract.SpecVersion)
	}
	checks := map[string]struct {
		got  []string
		want []string
	}{
		"agent_kind":          {[]string{string(Claude), string(Codex), string(Kimi), string(TraeX), string(OpenCode)}, contract.AgentKind},
		"session_state":       {[]string{string(Idle), string(Running), string(Blocked)}, contract.SessionState},
		"blocked_kind":        {[]string{string(BlockedUserInput), string(BlockedToolApproval), string(BlockedPermission), string(BlockedAuthentication), string(BlockedExternal)}, contract.BlockedKind},
		"event_kind":          {[]string{string(KindThinking), string(KindText), string(KindToolCall), string(KindToolResult)}, contract.EventKind},
		"error_kind":          {[]string{string(errs.KindTransport), string(errs.KindProtocol), string(errs.KindTimeout), string(errs.KindRuntimeReported), string(errs.KindUnsupported), string(errs.KindNotInstalled), string(errs.KindInvalidState), string(errs.KindSessionNotFound)}, contract.ErrorKind},
		"session_data_format": {[]string{string(SessionDataJSONL)}, contract.SessionDataFormat},
		"driver_transport":    {[]string{"fake", "acp_native", "codex_app_server", "claude_stream_json"}, contract.DriverTransport},
		"driver_lifecycle":    {[]string{string(driver.PhaseIdle), string(driver.PhaseStarting), string(driver.PhaseActive), string(driver.PhasePromptInFlight), string(driver.PhaseBlocked), string(driver.PhaseClosed), string(driver.PhaseFailed)}, contract.DriverLifecycle},
		"driver_event_type":   {[]string{string(driver.EventLifecycle), string(driver.EventSessionAttached), string(driver.EventOutput), string(driver.EventBlocked), string(driver.EventCompleted), string(driver.EventFailed)}, contract.DriverEventType},
		"finish_reason":       {[]string{string(driver.FinishNatural), string(driver.FinishCancelled), string(driver.FinishTransportClosed)}, contract.FinishReason},
	}
	for name, check := range checks {
		if !reflect.DeepEqual(check.got, check.want) {
			t.Errorf("%s: got %v want %v", name, check.got, check.want)
		}
	}
}
