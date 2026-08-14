# Keep remote discovery agentless

Remote Listener discovery will run as an ephemeral, unprivileged scanner streamed through the existing system-SSH session rather than as an installed remote binary or service. It observes only the SSH session's namespaces and degrades independently from Manual Forward capability when `/proc`, `ss`, `lsof`, or process metadata are unavailable. This limits remote footprint and lifecycle responsibility at the cost of Linux-only discovery and less visibility into containers or restricted processes.
