# JSON output

List, lookup, stop and update accept `--json` and print exactly one document on stdout, errors included. `--help`, `--version` and `completion` always print text. The schema is [`schema/portly.v1.schema.json`](../schema/portly.v1.schema.json). Within 1.x fields are added, never changed or removed. Values that can't be read are `null`.

| Command | Fields besides `schema_version`, `command`, `error`, `warnings` |
|---|---|
| `portly --json` | `ports`, `hidden_udp` |
| `portly 8000 --json` | `ports` |
| `portly stop 8000 --json` | `plan_id`, `targets`, `results` |
| `portly update --json` | `update`: `current`, `latest`, `status` (`updated`, `up_to_date`, `confirmation_required`) |

`stop --json` never prompts. Without `--yes` it stops nothing, returns `confirmation_required` and a `plan_id`. If every port is refused, `error` and `plan_id` are `null` and the reasons are in `results[].error`. Passing that id to `--expect` refuses the whole command if the targets have changed. An empty or malformed id is a usage error. The id is not a secret and authorizes nothing.

`update --json` never prompts either. Without `--yes` it changes nothing and returns `confirmation_required` when a newer release exists.

Each entry in `results` is one named port and points to a target by index. Each target lists the other endpoints and child processes it affects. After execution, `outcome` says what happened.

## Exit status and reason codes

| Exit | Codes |
|---|---|
| 0 | none. Also a lookup of a free port |
| 1 | `internal`, `update_unavailable`, `checksum_mismatch` |
| 2 | `usage` |
| 3 | `not_listening` |
| 4 | `permission_denied`, `unresolved_owner`, `evidence_unavailable`, `evidence_unstable`, `descendant_unreadable`, `engine_unresponsive` |
| 5 | `ambiguous_owner`, `protected_process`, `port_forwarder`, `supervised_process`, `container_managed`, `container_unproven`, `container_self`, `refused_with_other_port`, `target_changed`, `no_graceful_method`, `being_debugged`, `stop_signal_blocked`, `engine_rejected_stop`, `platform_unverified`, `package_managed`, `dev_build` |
| 6 | `confirmation_required`, `cancelled` |
| 7 | `draining`, `still_listening`, `restarted`, `taken_over`, `pending`, `unknown` |
| 8 | several ports with different exit statuses |
