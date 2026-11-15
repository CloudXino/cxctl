package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type result struct {
	out, err string
	code     int
}

// run runs cxctl against the fake API with the test key.
func run(t *testing.T, f *fakeAPI, stdin string, args ...string) result {
	t.Helper()
	return runRaw(t, stdin, append([]string{"--api-url", f.url(), "--token", f.token}, args...)...)
}

func runRaw(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Main("test", args, strings.NewReader(stdin), &out, &errOut)
	return result{out.String(), errOut.String(), code}
}

func mustRun(t *testing.T, f *fakeAPI, args ...string) string {
	t.Helper()
	r := run(t, f, "", args...)
	if r.code != 0 {
		t.Fatalf("cxctl %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), r.code, r.out, r.err)
	}
	return r.out
}

func contains(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("output does not contain %q:\n%s", w, got)
		}
	}
}

func TestAuthLoginSavesKeyAndUsesIt(t *testing.T) {
	f := newFakeAPI(t)
	cfgPath := os.Getenv("CXCTL_CONFIG")

	// a wrong key is checked and not saved
	r := runRaw(t, "cxk_wrong\n", "--api-url", f.url(), "auth", "login", "--name", "bad")
	if r.code != 1 || !strings.Contains(r.err, "Invalid API key") {
		t.Fatalf("bad key: %+v", r)
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Fatalf("config written for a bad key")
	}

	r = runRaw(t, f.token+"\n", "--api-url", f.url(), "auth", "login", "--name", "work")
	if r.code != 0 {
		t.Fatalf("login: %+v", r)
	}
	contains(t, r.out, `Acme`, `"work"`, "developer")
	st, err := os.Stat(cfgPath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("config mode: %v %v", st.Mode(), err)
	}

	// no --token / env: the saved login (and its API address) is used
	r = runRaw(t, "", "account")
	if r.code != 0 {
		t.Fatalf("account: %+v", r)
	}
	contains(t, r.out, "dev@example.com", "Acme", "cxk_ab12")

	r = runRaw(t, "", "auth", "list")
	contains(t, r.out, "*", "work", "cxk_ab12")
	if strings.Contains(r.out, f.token) {
		t.Errorf("auth list shows the key: %s", r.out)
	}

	if r = runRaw(t, "", "auth", "logout"); r.code != 0 {
		t.Fatalf("logout: %+v", r)
	}
	r = runRaw(t, "", "account")
	if r.code != 1 || !strings.Contains(r.err, "not logged in") {
		t.Fatalf("after logout: %+v", r)
	}
}

func TestServerCreateResolvesNamesAndWaits(t *testing.T) {
	f := newFakeAPI(t)
	mustRun(t, f, "ssh-key", "add", "--name", "laptop", "--public-key-file", writeFile(t, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJ me@laptop\n"))
	keyID := strings.TrimSpace(mustRun(t, f, "ssh-key", "get", "laptop", "-o", "id"))

	r := run(t, f, "", "server", "create", "--name", "web-1", "--image", "debian-13", "--size", "s-1vcpu-1gb",
		"--ssh-key", "laptop", "--project", "Staging", "--tag", "web", "--wait")
	if r.code != 0 {
		t.Fatalf("create: %+v", r)
	}
	contains(t, r.out, "web-1", "running", "203.0.113.20")
	contains(t, r.err, "Waiting for web-1")

	srv := f.list("mc2")[0]
	if srv["project_id"] != "p2" {
		t.Errorf("project_id = %v, want p2", srv["project_id"])
	}
	if ids, _ := srv["ssh_key_ids"].([]any); len(ids) != 1 || ids[0] != keyID {
		t.Errorf("ssh_key_ids = %v, want [%s]", srv["ssh_key_ids"], keyID)
	}
	if len(f.requests("POST /v1/mc2")) != 1 {
		t.Errorf("create sent more than once: %v", f.requests("POST"))
	}

	var list []map[string]any
	if err := json.Unmarshal([]byte(mustRun(t, f, "server", "list", "-o", "json")), &list); err != nil || len(list) != 1 {
		t.Fatalf("list -o json: %v %v", list, err)
	}
	if got := strings.TrimSpace(mustRun(t, f, "server", "list", "-o", "id")); got != srv["id"] {
		t.Errorf("list -o id = %q", got)
	}
	contains(t, mustRun(t, f, "server", "list", "--project", "Staging"), "NAME", "web-1")
	if out := mustRun(t, f, "server", "list", "--no-header"); strings.Contains(out, "NAME") {
		t.Errorf("--no-header printed a header: %s", out)
	}
}

func TestServerActionsAndDelete(t *testing.T) {
	f := newFakeAPI(t)
	f.put("mc2", map[string]any{"id": "s1", "name": "db", "status": "running", "size": "s-1vcpu-1gb"})

	contains(t, mustRun(t, f, "server", "stop", "db"), "Powering off")
	contains(t, mustRun(t, f, "server", "start", "db", "--wait"), "running")
	contains(t, mustRun(t, f, "server", "resize", "db", "--size", "s-2vcpu-2gb", "--wait"), "s-2vcpu-2gb", "running")
	contains(t, mustRun(t, f, "server", "update", "db", "--tag", "a", "--tag", "b"), "a,b")
	contains(t, mustRun(t, f, "server", "private-key", "db"), "BEGIN OPENSSH PRIVATE KEY")

	// answering anything but yes keeps the server
	r := run(t, f, "no\n", "server", "delete", "db")
	if r.code == 0 || !strings.Contains(r.err, "cancelled") || f.store["mc2"]["s1"] == nil {
		t.Fatalf("delete without yes: %+v", r)
	}
	contains(t, r.err, "Deleting db (with its disk) cannot be undone")

	r = run(t, f, "", "server", "delete", "db", "--yes", "--wait")
	if r.code != 0 {
		t.Fatalf("delete: %+v", r)
	}
	contains(t, r.out, "Deleted db (s1)")
	if _, ok := f.store["mc2"]["s1"]; ok {
		t.Error("server still there")
	}
}

func TestErrorsAndExitCodes(t *testing.T) {
	f := newFakeAPI(t)
	r := run(t, f, "", "server", "create", "--name", "x", "--image", "debian-13", "--size", "nope")
	if r.code != 1 || strings.TrimSpace(r.err) != "Error: Unknown size 'nope' (validation_error)" {
		t.Errorf("API error: %+v", r)
	}
	r = run(t, f, "", "server", "create", "--name", "x")
	if r.code != 2 || !strings.Contains(r.err, "--name, --image and --size are required") {
		t.Errorf("usage error: %+v", r)
	}
	r = run(t, f, "", "server", "list", "-o", "yaml")
	if r.code != 2 {
		t.Errorf("bad -o: %+v", r)
	}
	r = run(t, f, "", "server", "get", "missing")
	if r.code != 1 || !strings.Contains(r.err, `no server with ID or name "missing"`) {
		t.Errorf("not found: %+v", r)
	}

	f.put("mc2", map[string]any{"id": "a1", "name": "twin"})
	f.put("mc2", map[string]any{"id": "a2", "name": "twin"})
	r = run(t, f, "", "server", "get", "twin")
	if r.code != 1 || !strings.Contains(r.err, `2 servers are named "twin"; use an ID: a1, a2`) {
		t.Errorf("ambiguous: %+v", r)
	}
	contains(t, mustRun(t, f, "server", "get", "a2"), "a2")
}

func TestOptions(t *testing.T) {
	f := newFakeAPI(t)
	contains(t, mustRun(t, f, "server", "sizes"), "s-1vcpu-1gb", "360.00 BDT", "576.00 BDT")
	contains(t, mustRun(t, f, "server", "regions"), "dhaka-1", "Dhaka 1")
	contains(t, mustRun(t, f, "server", "images"), "debian-13", "Debian 13")
	var raw []map[string]any
	if err := json.Unmarshal([]byte(mustRun(t, f, "server", "sizes", "-o", "json")), &raw); err != nil || raw[0]["price_per_hour"] != 0.5 {
		t.Errorf("sizes -o json keeps every field: %v %v", raw, err)
	}
}

func TestFirewall(t *testing.T) {
	f := newFakeAPI(t)
	f.put("mc2", map[string]any{"id": "s1", "name": "web-1"})
	out := mustRun(t, f, "firewall", "create", "--name", "web",
		"--inbound", "tcp:80,443", "--inbound", "tcp:22:203.0.113.7/32,198.51.100.0/24", "--inbound", "icmp:0.0.0.0/0",
		"--server", "web-1", "--tag", "web")
	contains(t, out, "tcp:80,443:0.0.0.0/0", "tcp:22:203.0.113.7/32,198.51.100.0/24", "icmp:0.0.0.0/0", "s1")

	fw := f.list("firewalls")[0]
	b, _ := json.Marshal(fw["inbound_rules"])
	want := `[{"ports":"80,443","protocol":"tcp","sources":["0.0.0.0/0"]},{"ports":"22","protocol":"tcp","sources":["203.0.113.7/32","198.51.100.0/24"]},{"protocol":"icmp","sources":["0.0.0.0/0"]}]`
	if string(b) != want {
		t.Errorf("inbound rules sent:\n%s\nwant\n%s", b, want)
	}
	if _, ok := fw["outbound_rules"]; ok {
		t.Error("outbound rules sent although none were given")
	}

	// update replaces only the lists given
	mustRun(t, f, "firewall", "update", "web", "--inbound", "tcp:443", "--tag=")
	if tags := fw["tags"].([]any); len(tags) != 0 {
		t.Errorf("tags = %v", tags)
	}
	if ids := fw["server_ids"].([]any); len(ids) != 1 {
		t.Errorf("server_ids changed: %v", ids)
	}
	contains(t, mustRun(t, f, "firewall", "list"), "web", "active")
	mustRun(t, f, "firewall", "delete", "web", "-y")
	if len(f.list("firewalls")) != 0 {
		t.Error("firewall not deleted")
	}
}

func TestParseRule(t *testing.T) {
	for in, want := range map[string]string{
		"tcp":              `{"protocol":"tcp","sources":["0.0.0.0/0"]}`,
		"TCP:all":          `{"ports":"all","protocol":"tcp","sources":["0.0.0.0/0"]}`,
		"udp:53:1.1.1.1":   `{"ports":"53","protocol":"udp","sources":["1.1.1.1"]}`,
		"icmp::10.0.0.0/8": `{"protocol":"icmp","sources":["10.0.0.0/8"]}`,
	} {
		r, err := parseRule(in, "sources")
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		b, _ := json.Marshal(r)
		if string(b) != want {
			t.Errorf("%s: %s, want %s", in, b, want)
		}
	}
	for _, bad := range []string{"", "sctp:22", "tcp:1:2:3", "icmp:22:0.0.0.0/0"} {
		if _, err := parseRule(bad, "sources"); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestLoadBalancer(t *testing.T) {
	f := newFakeAPI(t)
	f.put("mc2", map[string]any{"id": "s1", "name": "web-1"})
	f.put("mc2", map[string]any{"id": "s2", "name": "web-2"})
	out := mustRun(t, f, "lb", "create", "--name", "front", "--rule", "http:80:8080", "--rule", "tcp:443:tcp:8443",
		"--health-check", "http:8080:/healthz", "--target", "web-1", "--wait")
	contains(t, out, "front", "active", "203.0.113.50", "http:80 -> http:8080", "tcp:443 -> tcp:8443", "http:8080:/healthz")

	lb := f.list("load-balancers")[0]
	b, _ := json.Marshal(lb["health_check"])
	if string(b) != `{"path":"/healthz","port":8080,"protocol":"http"}` {
		t.Errorf("health check sent: %s", b)
	}
	mustRun(t, f, "lb", "add-target", "front", "web-2")
	if ids := lb["target_ids"].([]any); len(ids) != 2 || ids[1] != "s2" {
		t.Errorf("targets after add: %v", ids)
	}
	mustRun(t, f, "lb", "remove-target", "front", "s1")
	if ids := lb["target_ids"].([]any); len(ids) != 1 || ids[0] != "s2" {
		t.Errorf("targets after remove: %v", ids)
	}
	r := run(t, f, "", "lb", "create", "--name", "x", "--rule", "http:eighty:8080")
	if r.code != 2 {
		t.Errorf("bad rule: %+v", r)
	}
	mustRun(t, f, "lb", "delete", "front", "--yes")
}

func TestDNS(t *testing.T) {
	f := newFakeAPI(t)
	r := run(t, f, "", "dns", "zone", "create", "example.com")
	if r.code != 0 {
		t.Fatalf("zone create: %+v", r)
	}
	contains(t, r.err, "Set these nameservers at the registrar of example.com: ns1.example.net,ns2.example.net")

	contains(t, mustRun(t, f, "dns", "record", "create", "example.com", "--name", "www", "--type", "A", "--content", "203.0.113.10", "--ttl", "300"),
		"www.example.com", "203.0.113.10", "300")
	mustRun(t, f, "dns", "record", "create", "example.com", "--type", "MX", "--content", "mail.example.com", "--priority", "10")

	// update by the record's name keeps what is not given
	contains(t, mustRun(t, f, "dns", "record", "update", "example.com", "www", "--content", "203.0.113.11"), "203.0.113.11", "300")
	rec := f.list("records")[0]
	if rec["type"] != "A" || rec["ttl"] != float64(300) {
		t.Errorf("update lost fields: %v", rec)
	}
	contains(t, mustRun(t, f, "dns", "record", "list", "example.com"), "www", "MX", "mail.example.com")
	contains(t, mustRun(t, f, "dns", "zone", "export", "example.com"), "$ORIGIN example.com.")

	r = run(t, f, "zone text", "dns", "zone", "import", "example.com", "-f", "-")
	if r.code != 0 || f.list("records")[2]["content"] != "zone text" {
		t.Errorf("import: %+v", r)
	}
	mustRun(t, f, "dns", "record", "delete", "example.com", "www", "-y")
	if len(f.list("records")) != 2 {
		t.Errorf("records after delete: %d", len(f.list("records")))
	}
	contains(t, mustRun(t, f, "dns", "zone", "list"), "example.com", "pending")
	mustRun(t, f, "dns", "zone", "delete", "example.com", "-y")
}

func TestSSHKeyAndProjects(t *testing.T) {
	f := newFakeAPI(t)
	mustRun(t, f, "ssh-key", "add", "--name", "laptop", "--public-key-file", writeFile(t, "ssh-ed25519 AAAAC3Nz me\n"))
	contains(t, mustRun(t, f, "ssh-key", "list"), "laptop", "SHA256:AAAAC3")
	contains(t, mustRun(t, f, "ssh-key", "get", "SHA256:AAAAC3"), "laptop")
	contains(t, mustRun(t, f, "ssh-key", "rename", "laptop", "work"), "work")
	mustRun(t, f, "ssh-key", "delete", "work", "--yes")
	contains(t, mustRun(t, f, "project", "list"), "Default", "yes", "Staging")
	contains(t, mustRun(t, f, "project", "get", "p2"), "Staging")
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := t.TempDir() + "/f"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReservedIP(t *testing.T) {
	f := newFakeAPI(t)
	f.put("mc2", map[string]any{"id": "srv-a", "name": "web-1", "status": "running", "region": "chittagong-1"})
	f.put("mc2", map[string]any{"id": "srv-b", "name": "web-2", "status": "running", "region": "chittagong-1"})

	// --server takes a name and the server's region
	out := mustRun(t, f, "reserved-ip", "create", "--server", "web-1", "--project", "Staging")
	contains(t, out, "assigned", "web-1", "chittagong-1")
	rip := f.list("reserved-ips")[0]
	addr := rip["ip_address"].(string)
	if rip["project_id"] != "p2" || rip["server_id"] != "srv-a" {
		t.Errorf("created %v", rip)
	}

	contains(t, mustRun(t, f, "reserved-ip", "list"), "ADDRESS", addr, "web-1")
	if got := strings.TrimSpace(mustRun(t, f, "rip", "list", "--ip", addr, "-o", "id")); got != rip["id"] {
		t.Errorf("list --ip -o id = %q", got)
	}

	// named by address; assign moves it
	contains(t, mustRun(t, f, "reserved-ip", "assign", addr, "web-2"), "web-2", "srv-b")
	if rip["server_id"] != "srv-b" {
		t.Errorf("not moved: %v", rip)
	}
	contains(t, mustRun(t, f, "reserved-ip", "get", rip["id"].(string)), addr, "web-2")
	contains(t, mustRun(t, f, "reserved-ip", "unassign", addr), "available")
	if rip["server_id"] != nil {
		t.Errorf("still assigned: %v", rip)
	}

	// delete asks first; with no terminal it stops
	if r := run(t, f, "", "reserved-ip", "delete", addr); r.code == 0 || len(f.list("reserved-ips")) != 1 {
		t.Fatalf("deleted without --yes: %+v", r)
	}
	r := run(t, f, "", "reserved-ip", "delete", addr, "--yes")
	if r.code != 0 || len(f.list("reserved-ips")) != 0 {
		t.Fatalf("delete: %+v", r)
	}
	contains(t, r.err+r.out, "Deleted reserved IP "+addr)

	if r := run(t, f, "", "reserved-ip", "get", "192.0.2.1"); r.code != 1 || !strings.Contains(r.err, `no reserved IP with ID or name "192.0.2.1"`) {
		t.Errorf("unknown address: %+v", r)
	}
}

func TestReservedIPUnavailable(t *testing.T) {
	f := newFakeAPI(t)
	f.ripsOff = true
	r := run(t, f, "", "reserved-ip", "create", "--region", "dhaka-1")
	if r.code != 1 || !strings.Contains(r.err, "Reserved IPs are not available right now (unavailable)") {
		t.Fatalf("create: %+v", r)
	}
	if n := len(f.requests("POST /v1/reserved-ips")); n != 1 {
		t.Errorf("sent %d times; 503 unavailable must not be retried", n)
	}
	contains(t, mustRun(t, f, "reserved-ip", "--help"), "create", "assign", "unassign", "billed per hour")
}
