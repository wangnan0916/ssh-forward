package cli

import (
	"flag"
	"fmt"
	"strconv"
	"strings"

	"ssh-forward/cli/internal/core"
)

// commandFlags is the shared flag set for resource commands: --json,
// --family, and --operation-id appear identically on forward add,
// listener approve, and listener suppress. The remote port is the one
// thing those commands name, so it is a positional argument, not a flag
// ("forward add 8080"), with flags preceding it.
type commandFlags struct {
	jsonOutput  *bool
	familyText  *string
	operationID *string
}

func newCommandFlags(set *flag.FlagSet) *commandFlags {
	return &commandFlags{
		jsonOutput:  set.Bool("json", false, "emit the wire-shaped outcome"),
		familyText:  set.String("family", "auto", "auto, ipv4, or ipv6"),
		operationID: set.String("operation-id", "", "stable operation ID for retries"),
	}
}

// boolFlag mirrors flag.boolFlag: flags whose Value implements it take no
// separate value token.
type boolFlag interface {
	IsBoolFlag() bool
}

// parseResourceFlags parses a resource command's flags with positional
// arguments allowed anywhere: "add 8080 --json" and "add --json 8080"
// behave alike. The positional arguments are returned separately.
func parseResourceFlags(set *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	var flags []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			positional = append(positional, args[index+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if equals := strings.IndexByte(name, '='); equals >= 0 {
			continue // value is inline
		}
		if lookup := set.Lookup(name); lookup != nil {
			if value, ok := lookup.Value.(boolFlag); !ok || !value.IsBoolFlag() {
				index++ // the next token is this flag's value
				if index < len(args) {
					flags = append(flags, args[index])
				}
			}
		}
	}
	if err := set.Parse(flags); err != nil {
		return nil, err
	}
	return positional, nil
}

// positionalPort parses the single positional remote-port argument the
// resource commands share, e.g. "add 8080".
func positionalPort(rest []string, command string) (uint16, error) {
	if len(rest) != 1 {
		return 0, fmt.Errorf("%s requires one remote port 1..65535", command)
	}
	port, err := strconv.ParseUint(rest[0], 10, 16)
	if err != nil || port == 0 {
		return 0, fmt.Errorf("%s requires one remote port 1..65535", command)
	}
	return uint16(port), nil
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
