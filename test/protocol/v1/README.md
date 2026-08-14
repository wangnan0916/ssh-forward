# IPC v1 golden transcripts

Each `.jsonl` file contains alternating client request and manager response frames without transport newlines inside a frame. Go and Swift Adapter tests consume the same files. Malformed UTF-8, oversized records, connection closure, and other non-JSON cases remain programmatic tests.
