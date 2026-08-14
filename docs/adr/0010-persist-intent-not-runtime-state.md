# Persist intent, not runtime state

The product will persist Development Host settings and Forwarding Policies but not Manual Forwards, one-time decisions, observations, live connections, or Active Forwards. After restart it will reconstruct policy-managed behavior from a fresh remote observation rather than replaying stale transport state. This preserves the distinction between durable user intent and volatile reality while keeping explicit one-off commands temporary.
