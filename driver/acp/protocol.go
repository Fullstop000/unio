package acp

import (
	"encoding/json"
	"fmt"
)

const protocolVersion = 1

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type rpcResponse struct {
	result json.RawMessage
	err    *rpcError
}

type capabilities struct {
	LoadSession bool
	List        bool
	Resume      bool
	Close       bool
}

type initializeResult struct {
	ProtocolVersion   int `json:"protocolVersion"`
	AgentCapabilities struct {
		LoadSession         bool `json:"loadSession"`
		SessionCapabilities struct {
			List   json.RawMessage `json:"list"`
			Resume json.RawMessage `json:"resume"`
			Close  json.RawMessage `json:"close"`
		} `json:"sessionCapabilities"`
	} `json:"agentCapabilities"`
}

func (r initializeResult) capabilities() capabilities {
	return capabilities{
		LoadSession: r.AgentCapabilities.LoadSession,
		List:        presentCapability(r.AgentCapabilities.SessionCapabilities.List),
		Resume:      presentCapability(r.AgentCapabilities.SessionCapabilities.Resume),
		Close:       presentCapability(r.AgentCapabilities.SessionCapabilities.Close),
	}
}

func presentCapability(raw json.RawMessage) bool {
	return len(raw) != 0 && string(raw) != "null"
}

func marshalRequest(id uint64, method string, params any) ([]byte, error) {
	return json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      uint64 `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{JSONRPC: "2.0", ID: id, Method: method, Params: params})
}

func marshalNotification(method string, params any) ([]byte, error) {
	return json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{JSONRPC: "2.0", Method: method, Params: params})
}

func marshalPermissionResponse(id json.RawMessage, outcome, optionID string) ([]byte, error) {
	result := map[string]any{"outcome": map[string]any{"outcome": outcome}}
	if optionID != "" {
		result["outcome"].(map[string]any)["optionId"] = optionID
	}
	var decodedID any
	if err := json.Unmarshal(id, &decodedID); err != nil {
		return nil, fmt.Errorf("invalid permission request id: %w", err)
	}
	return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": decodedID, "result": result})
}

func idKey(raw json.RawMessage) string {
	return string(raw)
}

func errorMessage(rpcErr *rpcError) string {
	if rpcErr == nil {
		return "unknown ACP error"
	}
	if rpcErr.Message != "" {
		return rpcErr.Message
	}
	return fmt.Sprintf("ACP error %d", rpcErr.Code)
}
