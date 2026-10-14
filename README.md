# voodu-fswapi

Voodu plugin for **FreeSWITCH**: a small HTTP API in front of FreeSWITCH's
event socket (ESL), so the voodu WebUI can show what FreeSWITCH is doing —
and, when you allow it, act on it.

| Piece | What | How it's deployed |
| --- | --- | --- |
| **FreeSWITCH** | your PBX / media server | your own `statefulset` (usually `network_mode = "host"`) — this plugin never owns it |
| **API** | this plugin — what the WebUI consumes | **`vd plugins:install` runs it**, as the builtin resource `plugins/fswapi`: a `deployment` on a **local** image (this binary + `Dockerfile.runtime`, built by the install hook — no public registry) |

The API is internal-only. The WebUI reaches it through the controller's
authenticated **PAT proxy** at `/api/pat/v1/fswapi/...`.
The name is `fswapi` on purpose: this plugin does **not** run or manage
FreeSWITCH instances — it only reads from and acts on one you already run.

## Dependencies

Everything below must be true before the API can answer.

**On the voodu host**

- **voodu controller ≥ v0.18.33.** It adds what this
  plugin is built on: builtin routes (`/api/pat/v1/fswapi/...` with no
  scope or name), the reserved `plugins` scope, `VOODU_CONTROLLER_URL` in
  the install hooks, and the PAT **`actions`** scope (plus the per-PAT rate
  limit) on plugin `POST`/`PUT`/`PATCH`/`DELETE` routes. On an older
  controller the install hook stops with an upgrade message.
- **docker** — the install hook builds the local image `voodu-fswapi`.
- **curl**, and egress to `github.com` + `release-assets.githubusercontent.com`
  — the install hook downloads the plugin binary from the GitHub release.

**On FreeSWITCH** (`autoload_configs/event_socket.conf.xml` + `acl.conf.xml`)

- **`mod_event_socket` loaded** (it is in the default `modules.conf.xml`).
- **`listen-ip` reachable from the `voodu0` network.** With FreeSWITCH on
  `network_mode = "host"`, `0.0.0.0` (or the host's docker bridge address)
  works: the API dials `host.docker.internal:8021`, which voodu maps to the
  host in every container. `127.0.0.1` alone is **not** reachable from a
  container.
- **An inbound ACL that admits the docker networks**, e.g.:

  ```xml
  <!-- acl.conf.xml -->
  <list name="esl_clients" default="deny">
    <node type="allow" cidr="127.0.0.1/32"/>
    <node type="allow" cidr="172.16.0.0/12"/>   <!-- docker bridges, voodu0 included -->
  </list>
  ```

  ```xml
  <!-- event_socket.conf.xml -->
  <param name="listen-ip" value="0.0.0.0"/>
  <param name="listen-port" value="8021"/>
  <param name="password" value="<a real password>"/>
  <param name="apply-inbound-acl" value="esl_clients"/>
  ```

- **The ESL password.** The API uses FreeSWITCH's default, `ClueCon`. If you
  changed it (you should), put the same value in the API's config bucket —
  never in HCL:

  ```bash
  vd config:set plugins/fswapi FREESWITCH_ESL_PASSWORD=<password>
  ```
- **Port 8021 closed to the outside world** (firewall / security group). ESL
  can run *any* FreeSWITCH command, including `hupall` and `shutdown`; only
  the host and its containers should reach it.

**For the WebUI**

- A PAT with **`read`** for the dashboard, and **`actions`** for any action.
  A read-only PAT can never run an action — the controller returns 403.

## Install

```bash
vd plugins:install thadeu/voodu-fswapi
vd config:set plugins/fswapi FREESWITCH_ESL_PASSWORD=<password>   # only if it is not ClueCon
```

That is all. The install hook downloads the binary, builds the image and runs
the API as **`plugins/fswapi`** with every default. No HCL file.

`plugins` is a scope the controller reserves for resources that plugins
create. You cannot put your own resources there, so `plugins/fswapi`
always means "made by this plugin".

| Command | Effect on `plugins/fswapi` |
| --- | --- |
| `vd plugins:install` (first time) | applied with the defaults |
| `vd plugins:install` / `vd plugins:update` (again) | restarted on the rebuilt image — its config is kept |
| `vd plugins:remove fswapi` | deleted, with the local images (the config bucket stays) |

## Change a default (optional)

Write a block **only** to change a default. Scope and name are fixed:

```hcl
# fswapi.voodu
fswapi "plugins" "fswapi" {
  esl     = "host.docker.internal:8021"   # default
  actions = ["reloadxml"]                  # default: none enabled

  resources {
    limits { cpu = "0.5", memory = "256Mi" }   # default: cpu 0.3, memory 128Mi
  }
}
```

```bash
vd apply -f fswapi.voodu
```

The block **is a deployment spec**: standard fields pass through untouched.
`expand` overlays only its own concerns, without clobbering yours: the local
image, `FREESWITCH_API_ADDR` (`0.0.0.0:8721` — **not 8080**, which is already
used inside the platform), `FREESWITCH_ESL_ADDR` (from `esl`),
`FREESWITCH_ACTIONS` (from `actions`), a `/health` check and the resource
limits (`cpu = "0.3"`, `memory = "128Mi"`). A `resources` block replaces the
default limits whole: set both values when you change one.

| Field | Plugin-only | Meaning |
| --- | --- | --- |
| `esl` | yes | `host:port` of the event socket. Default `host.docker.internal:8021`. |
| `actions` | yes | Actions this API may run. Default none. A typo fails `vd apply`. |

Any other scope or name fails `vd apply`: the PAT route always targets
`plugins/fswapi`, so an API anywhere else would be unreachable.

The kind is the plugin name, `fswapi`, so the block does not read like your
FreeSWITCH statefulset. If the plugin is not installed yet, `vd apply` installs
it from `thadeu/voodu-fswapi` first (the usual JIT install).

## API

All routes are reached through the controller PAT proxy:
`/api/pat/v1/fswapi/<route>` (e.g. `/api/pat/v1/fswapi/calls`).

### Reads (`GET`, PAT `read` scope)

| Route | FreeSWITCH command | Returns |
| --- | --- | --- |
| `/overview` | `status`, `show calls count`, `show channels count` (one connection) | `{status, calls, channels}` — the dashboard poll |
| `/status` | `status` | uptime, version, sessions (now / peak / since start), sessions per second, max, idle CPU, and the raw text |
| `/calls` · `/calls/count` | `show calls as json` · `show calls count` | bridged calls (A↔B = 1) |
| `/channels` · `/channels/count` | `show channels as json` · `show channels count` | call legs (A↔B = 2) |
| `/registrations` | `show registrations as json` | registered endpoints |
| `/sofia` | `sofia xmlstatus` | profiles, gateways and aliases with their state |
| `/actions` | — | every action, with `enabled` and `destructive` |
| `/health` | — | liveness of the API process (does not call FreeSWITCH) |

A FreeSWITCH that cannot be reached (dial, auth or ACL) answers **502** with
the reason. A wrong password also names the `vd config:set` command that
fixes it.

### Actions (`POST`, PAT `actions` scope)

There is **no generic "run a command" route.** Each action is named, builds
exactly one command from validated parameters, and is **off** until listed in
`actions`.

| Route | Command | Params (JSON body) | |
| --- | --- | --- | --- |
| `POST /actions/reloadxml` | `reloadxml` | — | |
| `POST /actions/reloadacl` | `reloadacl` | — | |
| `POST /actions/hupall` | `hupall <cause>` | `{"cause": "NORMAL_CLEARING"}` (`[A-Z_]`) | destructive |
| `POST /calls/{uuid}/kill` | `uuid_kill <uuid>` | — (uuid in the path) | destructive |

Answers: **200** `{ok: true, reply}`, **403** disabled (or not from the
controller), **404** unknown action, **422** invalid params or a `-ERR` reply
from FreeSWITCH, **502** FreeSWITCH unreachable.

Two locks on every action, on top of the allowlist:

1. **The controller:** the PAT must hold `actions`, and the per-PAT rate limit applies.
2. **This API:** it accepts a `POST` only from the `voodu0` gateway — where
   the controller's proxy comes from — so a neighbour container on `voodu0`
   cannot reach `/actions/hupall` directly. Override with
   `FREESWITCH_ACTION_SOURCES` (comma-separated CIDRs) only if you know why.

Every action attempt is logged by the API (`vd logs plugins/fswapi`).

## Environment

| Variable | Default | |
| --- | --- | --- |
| `FREESWITCH_API_ADDR` | `0.0.0.0:8721` | listen address (the PAT route reads its port) |
| `FREESWITCH_ESL_ADDR` | `host.docker.internal:8021` | event socket |
| `FREESWITCH_ESL_PASSWORD` | `ClueCon` | from the config bucket (`vd config:set plugins/fswapi ...`) |
| `FREESWITCH_ESL_TIMEOUT` | `5s` | per call |
| `FREESWITCH_ACTIONS` | empty | enabled actions |
| `FREESWITCH_ACTION_SOURCES` | the voodu0 gateway + loopback | who may `POST` |

## Manage the API

A plain `deployment` — use the generic `vd` commands:

```bash
vd get
vd logs plugins/fswapi
vd restart plugins/fswapi
vd config:set plugins/fswapi FREESWITCH_ESL_PASSWORD=<password>
```

Remove it with `vd plugins:remove fswapi`, not `vd delete`: the next
`plugins:install` would apply it again anyway.

## Development

```bash
make test
make build
make cross    # linux amd64 + arm64 release binaries together
```

## License

AGPL-3.0-only © Thadeu Esteves Jr
