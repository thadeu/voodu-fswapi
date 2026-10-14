# Changelog

All notable changes to voodu-fswapi are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/) and the project
adheres to [Semantic Versioning](https://semver.org/). The tag must match
`version:` in `plugin.yml`.

## [Unreleased]

## [0.1.1] - 2026-10-14

### Changed

- The API deployment gets default resource limits: `cpu = "0.3"`,
  `memory = "128Mi"`. A `resources` block in `fswapi "plugins" "fswapi"`
  replaces them.

## [0.1.0] - 2026-10-14

### Added

- `vd plugins:install` runs the API as the builtin resource
  `plugins/fswapi`, with defaults (no HCL): a deployment (local image,
  built by the install hook) serving JSON over FreeSWITCH's event socket,
  reached through the controller's PAT proxy at `/api/pat/v1/fswapi/...`.
  `plugins:update` restarts it on the rebuilt image; `plugins:remove`
  deletes it.
- `fswapi "plugins" "fswapi" { ... }` changes a default (`esl`,
  `actions`, any deployment field).
- The event_socket password defaults to FreeSWITCH's `ClueCon`; a wrong one
  answers 502 with the `vd config:set` command that fixes it.
- Reads: `/overview`, `/status`, `/calls`, `/calls/count`, `/channels`,
  `/channels/count`, `/registrations`, `/sofia`, `/actions`, `/health`.
- Actions (POST, each OFF until listed in `actions`): `reloadxml`,
  `reloadacl`, `hupall`, `uuid_kill`. Accepted only from the voodu0 gateway
  (the controller) by default.
