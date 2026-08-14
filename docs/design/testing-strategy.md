# Testing strategy

Formal implementation uses red-green TDD in vertical slices. Tests exercise behavior only through pre-agreed Seams:

1. `core.Manager` — `Execute`, `Snapshot`, `Watch`, and `Close` behavior, using private scripted OpenSSH/time Adapters.
2. OpenSSH Adapter — argv/environment construction, scanner/SOCKS lifecycle, exit classification, and process cleanup.
3. JSON-RPC Adapter — shared Go/Swift golden transcripts, framing bounds, version mismatch, typed errors, cancellation, Watch coalescing, and resync.
4. CLI — subprocess behavior and structured output against a real local Manager.
5. Proxy — real loopback half-close, cancellation, throughput, allocation, and optional remote end-to-end tests.
6. Swift Manager client and Dashboard state model — protocol fixtures and observable UI state transitions.

Tests do not address actor mailboxes, private matcher helpers, internal fields, or implementation call counts. Manager tests assert complete observable outcomes and survive changes to actor layout or Adapter wiring. Race, fuzz, leak, fault-injection, and benchmark runs supplement—not replace—behavioral slices.
