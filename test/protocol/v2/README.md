# IPC v2 golden transcripts

Request/response `.jsonl` transcripts contain alternating client request and manager response frames without transport newlines inside a frame. Files named `watch-notification` or `watch-resync-required` contain one expected manager notification. Go and future Swift Adapter tests consume the same corpus.
