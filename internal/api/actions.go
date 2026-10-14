package api

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Action is one operator command the API may run on FreeSWITCH. There is NO
// generic "run this api command" route: every action is named here, builds
// exactly one command, and validates its own parameters. An action is also
// OFF until the operator lists it (FREESWITCH_ACTIONS / the `actions` field),
// so installing the plugin never grants `hupall` by itself.
type Action struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Destructive bool   `json:"destructive"`

	build func(params map[string]string) (string, error)
}

var (
	reHangupCause = regexp.MustCompile(`^[A-Z_]{3,40}$`)
	reUUID        = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// catalog is every action this plugin knows. Adding one is adding an entry:
// a name, what it does, and how it builds its command from validated input.
var catalog = map[string]Action{
	"reloadxml": {
		Name: "reloadxml", Description: "Reload the XML configuration (dialplan, directory, configs).",
		build: func(map[string]string) (string, error) { return "reloadxml", nil },
	},
	"reloadacl": {
		Name: "reloadacl", Description: "Reload the access control lists.",
		build: func(map[string]string) (string, error) { return "reloadacl", nil },
	},
	"hupall": {
		Name: "hupall", Description: "Hang up EVERY active call (cause defaults to NORMAL_CLEARING).", Destructive: true,
		build: func(p map[string]string) (string, error) {
			cause := p["cause"]
			if cause == "" {
				cause = "NORMAL_CLEARING"
			}

			if !reHangupCause.MatchString(cause) {
				return "", fmt.Errorf("invalid hangup cause %q (want e.g. NORMAL_CLEARING)", cause)
			}

			return "hupall " + cause, nil
		},
	},
	"uuid_kill": {
		Name: "uuid_kill", Description: "Hang up ONE channel by its UUID.", Destructive: true,
		build: func(p map[string]string) (string, error) {
			if !reUUID.MatchString(p["uuid"]) {
				return "", fmt.Errorf("invalid channel uuid %q", p["uuid"])
			}

			return "uuid_kill " + strings.ToLower(p["uuid"]), nil
		},
	},
}

// Actions is the catalog filtered by what the operator enabled.
type Actions struct {
	enabled map[string]bool
}

// NewActions parses an allowlist ("reloadxml,hupall"). Unknown names are an
// error: a typo must fail the boot, not silently leave an action off.
func NewActions(list string) (Actions, error) {
	enabled := map[string]bool{}

	for _, raw := range strings.Split(list, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}

		if _, ok := catalog[name]; !ok {
			return Actions{}, fmt.Errorf("unknown action %q in FREESWITCH_ACTIONS (known: %s)", name, strings.Join(catalogNames(), ", "))
		}

		enabled[name] = true
	}

	return Actions{enabled: enabled}, nil
}

// Command builds the FreeSWITCH command for an enabled action.
func (a Actions) Command(name string, params map[string]string) (string, error) {
	action, ok := catalog[name]
	if !ok {
		return "", errUnknownAction
	}

	if !a.enabled[name] {
		return "", errActionDisabled
	}

	return action.build(params)
}

// List is every known action with its enabled flag, for a UI to draw the
// buttons it may offer.
func (a Actions) List() []map[string]any {
	out := make([]map[string]any, 0, len(catalog))

	for _, name := range catalogNames() {
		action := catalog[name]

		out = append(out, map[string]any{
			"name": action.Name, "description": action.Description,
			"destructive": action.Destructive, "enabled": a.enabled[name],
		})
	}

	return out
}

func catalogNames() []string {
	names := make([]string, 0, len(catalog))

	for name := range catalog {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}
