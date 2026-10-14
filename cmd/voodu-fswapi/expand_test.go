package main

import (
	"strings"
	"testing"
)

func expandFromJSON(t *testing.T, specJSON string) (expandedPayload, error) {
	t.Helper()

	spec, err := decodeSpecMap([]byte(specJSON))
	if err != nil {
		t.Fatalf("decodeSpecMap: %v", err)
	}

	return buildExpand("plugins", "fswapi", spec)
}

func onlyDeployment(t *testing.T, p expandedPayload) manifest {
	t.Helper()

	if len(p.Manifests) != 1 || p.Manifests[0].Kind != "deployment" {
		t.Fatalf("manifests = %+v, want exactly one deployment", p.Manifests)
	}

	return p.Manifests[0]
}

// The install hook applies an empty block: every default must come from here.
func TestExpand_Defaults(t *testing.T) {
	p, err := expandFromJSON(t, `{}`)
	if err != nil {
		t.Fatal(err)
	}

	m := onlyDeployment(t, p)
	env := m.Spec["env"].(map[string]any)

	if m.Scope != "plugins" || m.Name != "fswapi" {
		t.Errorf("resource = %s/%s, want plugins/fswapi", m.Scope, m.Name)
	}

	if m.Spec["image"] != "voodu-fswapi:latest" {
		t.Errorf("image = %v, want the stable local tag the install hook rebuilds", m.Spec["image"])
	}

	if env["FREESWITCH_API_ADDR"] != "0.0.0.0:8721" {
		t.Errorf("api addr = %v — must not be 8080", env["FREESWITCH_API_ADDR"])
	}

	if env["FREESWITCH_ESL_ADDR"] != "host.docker.internal:8021" {
		t.Errorf("esl addr = %v", env["FREESWITCH_ESL_ADDR"])
	}

	if _, ok := env["FREESWITCH_ACTIONS"]; ok {
		t.Error("no actions are enabled unless the operator lists them")
	}

	if _, ok := env["FREESWITCH_ESL_PASSWORD"]; ok {
		t.Error("the password comes from the config bucket, never from expand")
	}

	// The deployment's own bucket already reaches its env; an env_from to
	// itself fails the boot while that bucket is still empty.
	if _, ok := m.Spec["env_from"]; ok {
		t.Error("env_from must not be set by default")
	}

	if hc := m.Spec["health_check"].(string); hc != "wget -q -O- http://127.0.0.1:8721/health || exit 1" {
		t.Errorf("health_check = %q", hc)
	}
}

func TestExpand_PluginFieldsBecomeEnvAndAreStripped(t *testing.T) {
	p, err := expandFromJSON(t, `{"esl":"10.0.0.5:8021","actions":["reloadxml","hupall"]}`)
	if err != nil {
		t.Fatal(err)
	}

	m := onlyDeployment(t, p)
	env := m.Spec["env"].(map[string]any)

	if env["FREESWITCH_ESL_ADDR"] != "10.0.0.5:8021" || env["FREESWITCH_ACTIONS"] != "reloadxml,hupall" {
		t.Errorf("env = %v", env)
	}

	if _, ok := m.Spec["esl"]; ok {
		t.Error("`esl` is a plugin field, not a deployment field")
	}

	if _, ok := m.Spec["actions"]; ok {
		t.Error("`actions` is a plugin field, not a deployment field")
	}
}

func TestExpand_OperatorWins(t *testing.T) {
	p, err := expandFromJSON(t, `{"image":"my/fs-api:1","env":{"FREESWITCH_API_ADDR":"0.0.0.0:9000"},"resources":{"limits":{"memory":"64Mi"}}}`)
	if err != nil {
		t.Fatal(err)
	}

	m := onlyDeployment(t, p)
	env := m.Spec["env"].(map[string]any)

	if m.Spec["image"] != "my/fs-api:1" || env["FREESWITCH_API_ADDR"] != "0.0.0.0:9000" {
		t.Errorf("operator values lost: image=%v env=%v", m.Spec["image"], env)
	}

	if m.Spec["health_check"] != "wget -q -O- http://127.0.0.1:9000/health || exit 1" {
		t.Errorf("health_check must follow the operator's port: %v", m.Spec["health_check"])
	}

	if m.Spec["resources"] == nil {
		t.Error("standard fields must pass through")
	}
}

func TestExpand_RejectsBadActions(t *testing.T) {
	for _, spec := range []string{`{"actions":["hupal"]}`, `{"actions":"reloadxml"}`, `{"actions":[1]}`} {
		if _, err := expandFromJSON(t, spec); err == nil {
			t.Errorf("spec %s: want an error at apply time", spec)
		}
	}
}

// The PAT route always targets plugins/fswapi, so an API anywhere else
// would run unreachable. Refuse it and name the block that does work.
func TestExpand_OnlyTheBuiltinResource(t *testing.T) {
	for _, ref := range [][2]string{{"fsw", "fs-api"}, {"plugins", "api"}, {"plugins", "freeswitch"}, {"", "fswapi"}} {
		_, err := buildExpand(ref[0], ref[1], map[string]any{})
		if err == nil {
			t.Errorf("%s/%s: want an error", ref[0], ref[1])

			continue
		}

		if !strings.Contains(err.Error(), `fswapi "plugins" "fswapi"`) {
			t.Errorf("%s/%s: error should name the valid block, got %q", ref[0], ref[1], err)
		}
	}
}
