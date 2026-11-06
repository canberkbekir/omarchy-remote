package config

import (
	"os"
	"os/user"
	"strings"
)

// DefaultPCName names the PC the way phones and Macs do, like "Canberk's
// Desktop", since a hostname is often something like archlinux.
func DefaultPCName() string {
	data, _ := os.ReadFile("/sys/class/dmi/id/chassis_type")
	name := chassisName(strings.TrimSpace(string(data)))
	if owner := ownerName(); owner != "" {
		name = owner + "'s " + name
	}
	return name
}

// chassisName reads an SMBIOS chassis type.
func chassisName(code string) string {
	switch code {
	case "8", "9", "10", "14", "31", "32":
		return "Laptop"
	case "3", "4", "5", "6", "7", "15", "16", "24", "35", "36":
		return "Desktop"
	}
	return "PC"
}

// ownerName is the first name of the person logged in, or their username.
func ownerName() string {
	u, err := user.Current()
	if err != nil || u.Username == "root" {
		return ""
	}
	name := u.Username
	full, _, _ := strings.Cut(u.Name, ",")
	if words := strings.Fields(full); len(words) > 0 {
		name = words[0]
	}
	// Windows may name the account DOMAIN\user.
	return TitleCase(name[strings.LastIndex(name, `\`)+1:])
}

func TitleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
