package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/thadeu/voodu-fswapi/internal/api"
)

// imageRepo is the LOCAL image the install hook builds (binary + runtime
// Dockerfile). Never a public registry: the controller runs it without a pull
// because install built it on the node.
const imageRepo = "voodu-fswapi"

// imageTag is the stable tag expand references. Every install/update rebuilds
// it (next to a :<version> tag kept for traceability), and the hook restarts
// the API: the reconciler sees the tag now points at a new image ID and
// recreates the container. A versioned tag here would freeze the stored
// deployment on the first version, since nothing re-applies the block on
// update.
const imageTag = imageRepo + ":latest"

// builtinScope/builtinName is the one resource this plugin runs: the
// controller reserves the `plugins` scope for what plugins create, and the
// PAT route /api/pat/v1/fswapi/... always targets plugins/fswapi.
const (
	builtinScope = "plugins"
	builtinName  = "fswapi"
)

// cmdExpand reads the expand request and emits the API deployment.
func cmdExpand() error {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}

	req, err := decodeExpandRequest(raw)
	if err != nil {
		return err
	}

	spec, err := decodeSpecMap(req.Spec)
	if err != nil {
		return err
	}

	payload, err := buildExpand(req.Scope, req.Name, spec)
	if err != nil {
		return err
	}

	emitOK(payload)

	return nil
}

// buildExpand turns a `fswapi` block into the API deployment. The install
// hook applies an empty block; an operator applies one only to change a
// default:
//
//	fswapi "plugins" "fswapi" { actions = ["reloadxml"] }
//
// The block IS a deployment spec: every standard field (env, resources,
// networks, …) passes through unchanged. The plugin overlays only its own
// concerns and never clobbers what the operator set:
//
//   - image        → the local tag (operator may override)
//   - replicas     → 1 (operator may override)
//   - resources    → limits cpu 0.3, memory 128Mi (operator's block replaces
//     it whole)
//   - logs         → 2 files of 25m (operator's block replaces it whole)
//   - env          → FREESWITCH_API_ADDR, FREESWITCH_ESL_ADDR (from `esl`),
//     FREESWITCH_ACTIONS (from `actions`), merged UNDER the operator's env
//   - health_check → wget /health (operator may override)
//
// The password is not here: the deployment's own config bucket
// (`vd config:set plugins/fswapi ...`) reaches its env without env_from.
//
// Plugin-only fields, stripped from the deployment:
//
//   - esl     — "host:port" of the event socket (default host.docker.internal:8021)
//   - actions — the actions this API may run, e.g. ["reloadxml"]. Empty = none.
func buildExpand(scope, name string, spec map[string]any) (expandedPayload, error) {
	if scope != builtinScope || name != builtinName {
		return expandedPayload{}, fmt.Errorf(
			"the FreeSWITCH API runs once per server, as %s/%s (reached at /api/pat/v1/fswapi) — got %q/%q; to change its defaults, apply fswapi %q %q { ... }",
			builtinScope, builtinName, scope, name, builtinScope, builtinName)
	}

	dep := make(map[string]any, len(spec)+4)
	for k, v := range spec {
		dep[k] = v
	}

	eslAddr := strings.TrimSpace(asString(dep, "esl", api.DefaultESLAddr))
	delete(dep, "esl")

	actions, err := asStringList(dep, "actions")
	if err != nil {
		return expandedPayload{}, err
	}

	delete(dep, "actions")

	// Fail the APPLY on a typo, not the pod's boot minutes later.
	if _, err := api.NewActions(strings.Join(actions, ",")); err != nil {
		return expandedPayload{}, err
	}

	if _, ok := dep["image"]; !ok {
		dep["image"] = imageTag
	}

	if _, ok := dep["replicas"]; !ok {
		dep["replicas"] = 1
	}

	if _, ok := dep["resources"]; !ok {
		dep["resources"] = defaultResources()
	}

	if _, ok := dep["logs"]; !ok {
		dep["logs"] = defaultLogs()
	}

	env := map[string]any{
		"FREESWITCH_API_ADDR": api.DefaultAddr,
		"FREESWITCH_ESL_ADDR": eslAddr,
	}

	if len(actions) > 0 {
		env["FREESWITCH_ACTIONS"] = strings.Join(actions, ",")
	}

	for k, v := range asStringMap(dep, "env") {
		env[k] = v
	}

	dep["env"] = env

	if _, ok := dep["health_check"]; !ok {
		addr, _ := env["FREESWITCH_API_ADDR"].(string)
		dep["health_check"] = fmt.Sprintf("wget -q -O- http://127.0.0.1:%s/health || exit 1", portOf(addr, "8721"))
	}

	depManifest := manifest{Kind: "deployment", Scope: scope, Name: name, Spec: dep}

	return expandedPayload{Manifests: []manifest{depManifest}}, nil
}

// defaultResources is a low starting limit: the API holds no state and makes
// one short ESL connection per request. Raise it with a `resources` block.
func defaultResources() map[string]any {
	return map[string]any{
		"limits": map[string]any{"cpu": "0.3", "memory": "128Mi"},
	}
}

// defaultLogs caps the API's docker log at 2 files of 25m (~50MB): one line
// per request, plus every action attempt. Change it with a `logs` block.
func defaultLogs() map[string]any {
	return map[string]any{"max_size": "25m", "max_files": 2}
}

func portOf(addr, def string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 && i+1 < len(addr) {
		return addr[i+1:]
	}

	return def
}

func asString(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}

	return def
}

// asStringList reads a list-of-strings field. Absent = empty; anything else
// that isn't a list of strings is an error the operator must see.
func asStringList(m map[string]any, key string) ([]string, error) {
	raw, ok := m[key]
	if !ok || raw == nil {
		return nil, nil
	}

	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list of strings, got %T", key, raw)
	}

	out := make([]string, 0, len(items))

	for _, it := range items {
		s, ok := it.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be a list of strings, got an item of type %T", key, it)
		}

		out = append(out, strings.TrimSpace(s))
	}

	return out, nil
}

func asStringMap(m map[string]any, key string) map[string]string {
	raw, ok := m[key].(map[string]any)
	if !ok {
		return nil
	}

	out := make(map[string]string, len(raw))

	for k, v := range raw {
		out[k] = stringify(v)
	}

	return out
}

func stringify(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}

		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", x)
	}
}
