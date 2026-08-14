# Reference Development Hosts by SSH alias

The product will persist only the user's SSH alias as a Development Host reference and ask system OpenSSH to resolve and validate it, including `Include`, `Match`, proxy, identity, and agent behavior. It will not copy resolved hostnames, usernames, key paths, or network addresses into its own host profile. This keeps the user's SSH configuration authoritative and avoids building a competing SSH configuration manager.
