# Build independently from the legacy utility

This product is a greenfield implementation and will not reuse the existing shell repository, source, history, command contract, control socket, or forwarding runtime. The old utility remains installed and untouched while the new CLI, manager, and desktop app are developed in isolation; after the new product is mature, the user will perform any uninstall or cutover separately. This avoids carrying prototype constraints into the product at the cost of intentional backward incompatibility.
