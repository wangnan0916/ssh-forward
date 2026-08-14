# Version external contracts independently

Product releases, local manager IPC, JSONC configuration, and remote Listener Observation streams will each carry their own version. Clients reject unsupported protocol majors, configuration migrations create backups, and additive compatibility is defined per contract rather than inferred from the app version. This permits the bundled desktop, standalone CLI, manager, and saved policy files to evolve without silently misinterpreting one another.
