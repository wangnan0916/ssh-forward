package cli

import (
	"flag"
	"fmt"

	"ssh-forward/cli/internal/core"
)

// commandFlags is the shared flag set for resource commands: --json,
// --remote-port, --family, and --operation-id appear identically on
// forward add, listener approve, and listener suppress, and validation
// lives here so every command enforces the same rules (the wire adapter
// validates the same inputs for IPC clients).
type commandFlags struct {
	jsonOutput  *bool
	remotePort  *uint
	familyText  *string
	operationID *string
}

func newCommandFlags(set *flag.FlagSet) *commandFlags {
	return &commandFlags{
		jsonOutput:  set.Bool("json", false, "emit the wire-shaped outcome"),
		remotePort:  set.Uint("remote-port", 0, "remote port to target"),
		familyText:  set.String("family", "auto", "auto, ipv4, or ipv6"),
		operationID: set.String("operation-id", "", "stable operation ID for retries"),
	}
}

func (c *commandFlags) requireRemotePort() error {
	if *c.remotePort == 0 || *c.remotePort > 65535 {
		return fmt.Errorf("requires --remote-port 1..65535")
	}
	return nil
}

// family validates the --family flag exactly like the wire adapter does,
// so a bad family reports invalid parameters instead of a misleading
// Listener-not-found from the command path.
func (c *commandFlags) family() (core.AddressFamily, error) {
	family := core.AddressFamily(*c.familyText)
	if !core.ValidAddressFamily(family) {
		return "", fmt.Errorf("invalid --family %q (auto, ipv4, or ipv6)", *c.familyText)
	}
	return family, nil
}

func (c *commandFlags) operationIDOrRandom() core.CommandID {
	return core.CommandID(operationIDOrRandom(*c.operationID))
}
