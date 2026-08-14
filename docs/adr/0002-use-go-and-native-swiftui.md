# Use Go for the cross-platform core and native SwiftUI for macOS

The headless CLI core will be implemented in Go and target macOS, Linux, and eventually Windows, while the macOS desktop app will use SwiftUI with AppKit only where necessary. The app will communicate with a bundled, signed CLI/core over a versioned JSON protocol, and the core will continue delegating authentication and host verification to system OpenSSH through platform-specific session adapters. This favors simple native distribution and one behavioral core without imposing a cross-platform UI framework.
