# Run one manager per user

The new product will run at most one local manager for each OS user, shared by compatible standalone CLI and desktop clients. Each client starts only its own trusted executable by an absolute path; an incompatible client reports a restart or upgrade requirement and never kills an unknown manager. This centralizes port ownership and runtime truth while preventing PATH substitution and cross-version takeovers.
