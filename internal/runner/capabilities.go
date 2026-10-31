package runner

import (
	"strconv"
	"strings"
)

var requiredCapabilities = []struct {
	bit  uint
	name string
}{
	{0, "CHOWN"},
	{1, "DAC_OVERRIDE"},
	{3, "FOWNER"},
	{5, "KILL"},
	{6, "SETGID"},
	{7, "SETUID"},
}

func requiredCapabilityNames() []string {
	names := make([]string, 0, len(requiredCapabilities))
	for _, capability := range requiredCapabilities {
		names = append(names, capability.name)
	}
	return names
}

// parseMissingCapabilities reads the effective capability mask from the
// CapEff line of /proc/self/status. Anything it cannot parse is treated as 0,
// which makes startup fail closed.
func parseMissingCapabilities(status string) []string {
	var effective uint64
	for _, line := range strings.Split(status, "\n") {
		if !strings.HasPrefix(line, "CapEff:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 2 {
			effective, _ = strconv.ParseUint(fields[1], 16, 64)
		}
		break
	}
	missing := make([]string, 0, len(requiredCapabilities))
	for _, capability := range requiredCapabilities {
		if effective&(1<<capability.bit) == 0 {
			missing = append(missing, capability.name)
		}
	}
	return missing
}
