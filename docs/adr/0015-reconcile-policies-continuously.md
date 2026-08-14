# Reconcile policies continuously

Forwarding Policies will be reevaluated against every valid Listener Observation rather than only when a listener first appears. Policy-created Managed Forwards are added or removed after bounded evidence hysteresis, while One-time Approval lasts for one Listener Lifetime and Manual Forward remains outside policy reconciliation. Listener Lifetime follows socket continuity, not merely a stable port or PID, so a replacement service cannot inherit a one-time decision accidentally.
