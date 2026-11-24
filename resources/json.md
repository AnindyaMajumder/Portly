# JSON output

Every command accepts `--json` and prints exactly one document on stdout, errors included. The schema is [`schema/portly.v1.schema.json`](../schema/portly.v1.schema.json). Within 1.x fields are added, never changed or removed. Values that can't be read are `null`.

| Command | Fields besides `schema_version`, `command`, `error`, `warnings` |
|---|---|
| `portly --json` | `ports`, `hidden_udp` |
| `portly 8000 --json` | `ports` |
| `portly stop 8000 --json` | `plan_id`, `targets`, `results` |

`stop --json` never prompts. Without `--yes` it stops nothing, returns `confirmation_required` and a `plan_id`. Passing that id to `--expect` refuses the whole command if the targets have changed. The id is not a secret and authorizes nothing.

Each entry in `results` is one named port and points to a target by index. Each target lists the other endpoints and child processes it affects. After execution, `outcome` says what happened.

## Exit status and reason codes

| Exit | Codes |
|---|---|
| 0 | none. Also a lookup of a free port |
| 1 | `internal` |
| 2 | `usage` |
| 3 | `not_listening` |
| 4 | `permission_denied`, `unresolved_owner`, `evidence_unavailable`, `evidence_unstable`, `descendant_unreadable`, `engine_unresponsive` |
| 5 | `ambiguous_owner`, `protected_process`, `port_forwarder`, `supervised_process`, `container_managed`, `container_unproven`, `container_self`, `refused_with_other_port`, `target_changed`, `no_graceful_method`, `being_debugged`, `stop_signal_blocked`, `engine_rejected_stop`, `platform_unverified` |
| 6 | `confirmation_required`, `cancelled` |
| 7 | `draining`, `still_listening`, `restarted`, `taken_over`, `pending`, `unknown` |
| 8 | several ports with different exit statuses |
