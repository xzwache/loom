package permission

import (
	"path"
	"regexp"
	"strings"
)

var forkBomb = regexp.MustCompile(`(\w+|:)\s*\(\s*\)\s*\{[^}]*(\w+|:)\s*\|\s*(\w+|:)\s*&`)

var protectedDirs = map[string]bool{
	"/": true, "/*": true, "~": true, "~/": true, "~/*": true, "$HOME": true, "${HOME}": true,
	"/bin": true, "/boot": true, "/dev": true, "/etc": true, "/home": true, "/lib": true, "/Library": true,
	"/opt": true, "/sbin": true, "/System": true, "/usr": true, "/Users": true, "/var": true,
}

var devicePrefixes = []string{"/dev/sd", "/dev/hd", "/dev/nvme", "/dev/disk", "/dev/mmcblk", "/dev/vd"}

// floor returns why src must never run, in any mode and whatever the rules
// say, or "" when it may.
func floor(src string, cmds []command) string {
	if forkBomb.MatchString(src) {
		return "fork bomb"
	}
	for _, c := range cmds {
		for _, t := range c.writes {
			if isDevice(t) {
				return "writes to a disk device"
			}
		}
		words := stripWrappers(stripPrivilege(c.words))
		if len(words) == 0 {
			continue
		}
		name, args := words[0], words[1:]
		switch {
		case name == "rm" && recursive(args) && anyProtected(args):
			return "recursive delete of a system or home directory"
		case name == "mkfs" || strings.HasPrefix(name, "mkfs."):
			return "formats a file system"
		case name == "dd" && anyPrefix(args, "of=/dev/"):
			return "writes to a disk device"
		case name == "shutdown" || name == "reboot" || name == "halt" || name == "poweroff":
			return "shuts down the machine"
		case name == "init" && len(args) > 0 && (args[0] == "0" || args[0] == "6"):
			return "shuts down the machine"
		case name == "kill" && len(args) > 0 && args[len(args)-1] == "-1":
			return "kills every process"
		}
	}
	return ""
}

func stripPrivilege(words []string) []string {
	for len(words) > 0 {
		switch words[0] {
		case "sudo", "doas":
			words = skipOptions(words[1:], "-u", "-g", "-C", "-D", "-h", "-p", "-r", "-t", "-U")
		case "env":
			words = skipOptions(words[1:], "-u", "-C", "-S")
			for len(words) > 0 && strings.Contains(words[0], "=") {
				words = words[1:]
			}
		default:
			return words
		}
	}
	return words
}

func recursive(args []string) bool {
	for _, a := range args {
		if a == "--recursive" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a, "rR")) {
			return true
		}
	}
	return false
}

func anyProtected(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if protectedDirs[a] || (strings.HasPrefix(a, "/") && protectedDirs[path.Clean(a)]) {
			return true
		}
	}
	return false
}

func anyPrefix(args []string, prefix string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

func isDevice(target string) bool {
	for _, p := range devicePrefixes {
		if strings.HasPrefix(target, p) {
			return true
		}
	}
	return false
}
