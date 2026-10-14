// wire.go holds the controller<->plugin JSON contract shared by every
// command. It mirrors the voodu-postgres / voodu-redis shapes verbatim
// so the controller's dispatcher treats this plugin identically — when
// the pkg/plugin/sdk lands, this file is replaced by `import sdk`.
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// expandRequest is what the controller streams on stdin for `expand`.
// Config is the merged config bucket for (scope, name): empty on first
// apply, populated on later applies with whatever config_set actions the
// plugin emitted before.
type expandRequest struct {
	Kind   string            `json:"kind"`
	Scope  string            `json:"scope,omitempty"`
	Name   string            `json:"name"`
	Spec   json.RawMessage   `json:"spec,omitempty"`
	Config map[string]string `json:"config,omitempty"`
}

// envelope is the standard plugin stdout protocol. Status is "ok" or
// "error"; Data carries the command-specific payload.
type envelope struct {
	Status string `json:"status"`
	Data   any    `json:"data,omitempty"`
	Error  string `json:"error,omitempty"`
}

// manifest is one core kind the plugin asks the controller to reconcile.
// fswapi emits exactly one: a deployment.
type manifest struct {
	Kind  string         `json:"kind"`
	Scope string         `json:"scope,omitempty"`
	Name  string         `json:"name"`
	Spec  map[string]any `json:"spec"`
}

// expandedPayload is the envelope-data shape the controller's dispatcher
// recognises for expand: { manifests, actions }. fswapi emits no
// actions, so only manifests is carried.
type expandedPayload struct {
	Manifests []manifest `json:"manifests"`
}

// decodeExpandRequest parses the stdin payload for `expand`.
func decodeExpandRequest(raw []byte) (expandRequest, error) {
	var req expandRequest

	if err := json.Unmarshal(raw, &req); err != nil {
		return expandRequest{}, fmt.Errorf("decode expand request: %w", err)
	}

	return req, nil
}

// decodeSpecMap parses an HCL block body (JSON object) into a generic
// map. An empty body yields an empty map (all defaults apply).
func decodeSpecMap(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}

	var m map[string]any

	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode block spec: %w", err)
	}

	return m, nil
}

// emitOK writes a success envelope to stdout.
func emitOK(data any) {
	_ = json.NewEncoder(os.Stdout).Encode(envelope{Status: "ok", Data: data})
}

// emitErr writes an error envelope to stdout. Callers exit non-zero
// after calling this so the controller reports the failure.
func emitErr(msg string) {
	_ = json.NewEncoder(os.Stdout).Encode(envelope{Status: "error", Error: msg})
}
