package main

import "fmt"

// printPluginOverview prints the operator-facing overview — `vd fswapi -h`
// reaches us as "help".
func printPluginOverview() {
	fmt.Print(pluginOverview)
}

const pluginOverview = `voodu-fswapi — HTTP API over a FreeSWITCH event socket (ESL)

Installing the plugin runs the API as the builtin resource plugins/fswapi,
with defaults. The WebUI reaches it through the controller's PAT proxy at
/api/pat/v1/fswapi/... (read scope for GET, actions scope for POST).

  vd plugins:install thadeu/voodu-fswapi
  vd config:set plugins/fswapi FREESWITCH_ESL_PASSWORD=...   # if not ClueCon

Change a default only when you need to:

  fswapi "plugins" "fswapi" {
    esl     = "host.docker.internal:8021"   # default
    actions = ["reloadxml"]                 # default: none enabled
    resources { limits { cpu = "0.5", memory = "256Mi" } }   # default: 0.3 / 128Mi
    logs { max_size = "50m", max_files = 3 }                 # default: 25m x 2
  }

Reads:    /overview /status /calls /calls/count /channels /channels/count
          /channels/{uuid}/sip_call_id /registrations /sofia /actions
Actions:  POST /actions/{reloadxml|reloadacl|hupall}  POST /calls/{uuid}/kill
          (each OFF until listed in ` + "`actions`" + `)

Lifecycle: vd logs / restart plugins/fswapi. vd plugins:remove fswapi
deletes it.
`
