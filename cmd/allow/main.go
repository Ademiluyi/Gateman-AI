// Command allow adds, removes, or lists phone numbers in OpenClaw's
// channels.whatsapp.allowFrom policy AND in the gatemanai-camera MCP
// server's OPENCLAW_TO env var. WhatsApp Web refuses to send to any
// number not on the allowFrom list, and mcpserver only sends inbound
// photos to numbers listed in its env. Onboarding a tester therefore
// touches both places — this helper does both in one command and
// restarts the gateway so the change takes effect immediately.
//
// Usage:
//
//	allow +2347037220878             # add to both lists, restart the gateway
//	allow -remove +2347037220878     # remove from both lists, restart the gateway
//	allow -list                      # print both current lists
//	allow -no-restart +234...        # skip the gateway restart (useful for batches)
//	allow -mcp-server <name>         # use a different MCP server name (default: gatemanai-camera)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const defaultMCPServer = "gatemanai-camera"

func main() {
	list := flag.Bool("list", false, "list current allowFrom and MCP target numbers, then exit")
	remove := flag.Bool("remove", false, "remove the given number instead of adding")
	norestart := flag.Bool("no-restart", false, "skip the gateway restart after the change")
	configPath := flag.String("config", defaultConfigPath(), "path to openclaw.json")
	mcpServer := flag.String("mcp-server", defaultMCPServer, "MCP server name to update")
	flag.Parse()

	args := flag.Args()
	if !*list && len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: allow +<number>            add a number to both lists")
		fmt.Fprintln(os.Stderr, "       allow -remove +<number>    remove from both lists")
		fmt.Fprintln(os.Stderr, "       allow -list                show current state")
		os.Exit(2)
	}

	cfg, err := readConfig(*configPath)
	if err != nil {
		log.Fatalf("read config: %v", err)
	}

	allow := getAllowFrom(cfg)
	mcp := getMCPTargets(cfg, *mcpServer)

	if *list {
		fmt.Println("channels.whatsapp.allowFrom:")
		printList(allow)
		fmt.Printf("\nmcp.servers.%s.env.OPENCLAW_TO:\n", *mcpServer)
		printList(mcp)
		if drift := diff(allow, mcp); len(drift) > 0 {
			fmt.Printf("\n⚠ drift between the two lists: %s\n", strings.Join(drift, ", "))
		}
		return
	}

	target := strings.TrimSpace(args[0])
	if !strings.HasPrefix(target, "+") {
		log.Fatalf("number must be in E.164 format starting with +, got %q", target)
	}

	allowChanged := false
	mcpChanged := false

	switch {
	case *remove:
		if next, found := withoutEntry(allow, target); found {
			allow = next
			allowChanged = true
			fmt.Printf("− removed %s from allowFrom\n", target)
		}
		if next, found := withoutEntry(mcp, target); found {
			mcp = next
			mcpChanged = true
			fmt.Printf("− removed %s from %s OPENCLAW_TO\n", target, *mcpServer)
		}
		if !allowChanged && !mcpChanged {
			fmt.Printf("%s was not in either list; nothing to do\n", target)
			return
		}
	default:
		if !contains(allow, target) {
			allow = append(allow, target)
			allowChanged = true
			fmt.Printf("+ added %s to allowFrom\n", target)
		}
		if !contains(mcp, target) {
			mcp = append(mcp, target)
			mcpChanged = true
			fmt.Printf("+ added %s to %s OPENCLAW_TO\n", target, *mcpServer)
		}
		if !allowChanged && !mcpChanged {
			fmt.Printf("%s is already in both lists; nothing to do\n", target)
			return
		}
	}

	if allowChanged {
		setAllowFrom(cfg, allow)
	}
	if mcpChanged {
		setMCPTargets(cfg, *mcpServer, mcp)
	}
	if err := writeConfig(*configPath, cfg); err != nil {
		log.Fatalf("write config: %v", err)
	}
	fmt.Printf("  wrote %s\n", *configPath)

	if *norestart {
		fmt.Println("  skipping gateway restart (-no-restart). Run: openclaw gateway restart")
		return
	}

	fmt.Println("  restarting OpenClaw gateway...")
	cmd := exec.Command("openclaw", "gateway", "restart")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("gateway restart failed: %v", err)
	}
}

func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".openclaw", "openclaw.json")
}

func readConfig(path string) (map[string]any, error) {
	if path == "" {
		return nil, fmt.Errorf("config path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

func writeConfig(path string, cfg map[string]any) error {
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return os.WriteFile(path, out, 0644)
}

// getAllowFrom reads channels.whatsapp.allowFrom, returning an empty
// slice if any layer is missing.
func getAllowFrom(cfg map[string]any) []string {
	channels, _ := cfg["channels"].(map[string]any)
	whatsapp, _ := channels["whatsapp"].(map[string]any)
	raw, _ := whatsapp["allowFrom"].([]any)
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// setAllowFrom writes channels.whatsapp.allowFrom, creating intermediate
// objects if needed.
func setAllowFrom(cfg map[string]any, allow []string) {
	channels, _ := cfg["channels"].(map[string]any)
	if channels == nil {
		channels = map[string]any{}
		cfg["channels"] = channels
	}
	whatsapp, _ := channels["whatsapp"].(map[string]any)
	if whatsapp == nil {
		whatsapp = map[string]any{}
		channels["whatsapp"] = whatsapp
	}
	back := make([]any, 0, len(allow))
	for _, a := range allow {
		back = append(back, a)
	}
	whatsapp["allowFrom"] = back
}

// getMCPTargets reads mcp.servers.<name>.env.OPENCLAW_TO and parses the
// comma-separated value. Returns an empty slice if any layer is missing.
func getMCPTargets(cfg map[string]any, serverName string) []string {
	mcp, _ := cfg["mcp"].(map[string]any)
	servers, _ := mcp["servers"].(map[string]any)
	server, _ := servers[serverName].(map[string]any)
	env, _ := server["env"].(map[string]any)
	raw, _ := env["OPENCLAW_TO"].(string)
	return parseCSV(raw)
}

// setMCPTargets writes mcp.servers.<name>.env.OPENCLAW_TO as a
// comma-separated string, creating intermediate objects if needed.
// Refuses to create the server entry from scratch — the command and
// args are project-specific and we don't want to invent them silently.
func setMCPTargets(cfg map[string]any, serverName string, targets []string) {
	mcp, _ := cfg["mcp"].(map[string]any)
	if mcp == nil {
		log.Fatalf("openclaw.json has no mcp.servers section; register %s first via 'openclaw mcp set'", serverName)
	}
	servers, _ := mcp["servers"].(map[string]any)
	if servers == nil {
		log.Fatalf("openclaw.json has no mcp.servers section; register %s first via 'openclaw mcp set'", serverName)
	}
	server, _ := servers[serverName].(map[string]any)
	if server == nil {
		log.Fatalf("MCP server %q is not registered; register it first via 'openclaw mcp set %s ...'", serverName, serverName)
	}
	env, _ := server["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
		server["env"] = env
	}
	env["OPENCLAW_TO"] = strings.Join(targets, ",")
}

func parseCSV(s string) []string {
	var out []string
	for _, raw := range strings.Split(s, ",") {
		t := strings.TrimSpace(raw)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func withoutEntry(haystack []string, needle string) (out []string, found bool) {
	out = haystack[:0:0]
	for _, h := range haystack {
		if h == needle {
			found = true
			continue
		}
		out = append(out, h)
	}
	return out, found
}

// diff returns numbers in either list but not both — useful for the -list
// command to flag drift the operator may have introduced by editing one
// list directly.
func diff(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !contains(b, x) {
			out = append(out, x+" (only in allowFrom)")
		}
	}
	for _, x := range b {
		if !contains(a, x) {
			out = append(out, x+" (only in MCP env)")
		}
	}
	return out
}

func printList(xs []string) {
	if len(xs) == 0 {
		fmt.Println("  (empty)")
		return
	}
	for _, x := range xs {
		fmt.Printf("  %s\n", x)
	}
}
