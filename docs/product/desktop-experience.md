# Desktop experience

## Interface surfaces

At login the app presents only a menu-bar icon and does not remain in the Dock while idle. Its compact panel shows the selected host and connection state, counts and a short list of items needing attention, a compact Active Forward list, pause/reconnect controls, and actions to open the Dashboard, settings, or quit.

The Dashboard is the full management surface. It uses a regular window with host navigation, Listener/Forward lists, and a details inspector for process metadata, working directory, Policy evidence, logs, and diagnostics. Policy editing also uses the full window. The Dashboard opens from the menu panel, notifications, or a shortcut; a persistent Dock mode is deferred.

## State organization

The interface is organized by whether the user needs to act rather than by saved tunnel profiles:

1. **Needs Attention** — unmatched new Remote Listeners, Local Port Conflicts, and authentication errors.
2. **Active** — working Forwards, showing remote and Allocated Local Ports.
3. **Available** — observed listeners that are neither forwarded nor ignored.
4. **Ignored** — collapsed by default.

Listener rows show the remote port, Allocated Local Port when present, process summary, working-directory summary, and an explanation of the matching Policy or default decision. Host connection controls and settings remain directly reachable.

The first desktop release displays and automatically monitors one selected Development Host at a time, while the manager and protocols retain host identity and support multiple host sessions for later UI expansion.

IPv4 and IPv6 Remote Listeners retain separate domain identities. The UI may group same-port variants only when socket and process evidence proves they represent one service; missing or conflicting evidence keeps them separate. Policy evaluation always uses the underlying Listener Observations rather than the presentation group.

## Notifications

The Discovery Baseline is silent. Newly observed unmatched listeners may notify, and clicking the notification opens the corresponding item. Bursts are aggregated and rate-limited. Authentication errors, sustained reconnect failure, and Local Port Conflicts may notify. Notifications never include full remote paths or complete command arguments.

## Policy editing

The default Policy editor is a structured form that explains why current listeners match or fail to match. Before saving, it previews which current listeners would be affected. An advanced action opens `policies.jsonc` directly. Invalid JSONC leaves the original file untouched and reports precise line and column diagnostics.
