# Connection lifecycle

## Host resolution

A Development Host is stored by SSH alias only. The OpenSSH adapter validates and inspects effective configuration through system `ssh -G` (openssh/adapter.go); resolved hostname, username, identity paths, proxy settings, and addresses are not copied into a product-owned host profile.

## Launch at login and local manager

During onboarding, the desktop app explicitly offers a preselected **Launch at Login** option. On macOS 13 or later it registers only the main app through `SMAppService.mainApp`; the manager is not separately installed as a login item or system boot daemon.

Each Development Host has a **Monitor at Login** setting, enabled initially for the first host. At login, the app starts the manager and SSH only when at least one host requires monitoring. Otherwise the lightweight menu-bar app remains idle. Outside login startup, the manager starts on demand when a CLI or desktop operation needs it. With no Forwarding Sessions, Active Forwards, or observing clients, it exits after five idle minutes. Quitting the desktop does not terminate a manager that still owns Active Forwards.

## Reconnection

When an SSH transport disconnects, existing Local Endpoints remain allocated so other processes cannot claim their ports, but new client connections fail promptly rather than queueing indefinitely. In-flight proxy connections receive EOF or reset promptly; a bounded post-half-close drain prevents a client that ignores upstream EOF from retaining a proxy goroutine indefinitely. Reconnection uses exponential backoff with jitter through injectable clock/random behavior. Authentication and host-key failures are classified from bounded OpenSSH diagnostics and suspend automatic retries until the user acts. A session outage does not count toward Managed Forward disappearance; reconnection obtains a complete observation before cleanup resumes.

## Browser actions

The product does not probe an unknown service with HTTP requests. Browser actions are available only when the user or a Forwarding Policy declares the service protocol as HTTP or HTTPS; automatic browser opening must be an explicit policy action.

## Implementation status

The sections above state the full-product behavior. This map records which behaviors are enforced today and which land with later slices; it is the checklist that flips as surfaces land.

Enforced today:

- Effective SSH configuration is inspected through `ssh -G` and never copied into a product-owned profile (openssh/adapter.go).
- Local Endpoints remain allocated across a transport disconnect; new client connections fail promptly; in-flight proxies end on EOF/reset with a bounded post-half-close drain (proxy/).
- Reconnection uses exponential backoff with jitter through injectable clock/random behavior (core/manager.go, core/actor.go).
- Authentication and host-key failures are classified from bounded OpenSSH diagnostics and published as Connection Diagnostic on the Snapshot (core/actor.go, core/connection.go).
- ObservationSets advance policy reconciliation; session outage does not count toward Managed Forward disappearance (core/actor.go, core/reconcile.go).

Lands with later slices:

- Launch at Login, Monitor at Login, and the five-idle-minute exit: desktop phase (the CLI auto-spawns a manager that currently does not idle-exit).
- `SMAppService.mainApp` registration and manager-as-login-item placement: desktop phase.
- Browser actions and their Forwarding Policy declaration: desktop policy surfaces (docs/product/discovery-and-policy.md).
