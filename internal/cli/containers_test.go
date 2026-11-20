package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

const compose = "services:\n  web:\n    image: nginx:1.27\n"

// newHost creates a ready host named apps through the CLI.
func newHost(t *testing.T, f *fakeAPI) string {
	t.Helper()
	r := run(t, f, "", "containers", "create", "--name", "apps", "--plan", "s-2vcpu-4gb", "--project", "Staging", "--wait")
	if r.code != 0 {
		t.Fatalf("create: %+v", r)
	}
	contains(t, r.out, "apps", "ready", "203.0.113.40", "https://203-0-113-40.sslip.io", "2199.00 BDT")
	contains(t, r.err, "Waiting for apps")
	h := f.list("container-hosts")[0]
	if h["project_id"] != "p2" || h["plan"] != "s-2vcpu-4gb" {
		t.Fatalf("host %v", h)
	}
	return h["id"].(string)
}

func TestContainersCreateListGet(t *testing.T) {
	f := newFakeAPI(t)
	id := newHost(t, f)
	if n := len(f.requests("POST /v1/container-hosts")); n != 1 {
		t.Errorf("create sent %d times", n)
	}
	contains(t, mustRun(t, f, "containers", "list"), "NAME", "apps", "ready", "s-2vcpu-4gb", "dhaka-1")
	if got := strings.TrimSpace(mustRun(t, f, "containers", "list", "-o", "id")); got != id {
		t.Errorf("list -o id = %q", got)
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(mustRun(t, f, "containers", "list", "--project", "Default", "-o", "json")), &list); err != nil || len(list) != 0 {
		t.Errorf("list --project Default: %v %v", list, err)
	}
	contains(t, mustRun(t, f, "containers", "get", "apps"), "Portainer:", "https://203-0-113-40.sslip.io", "Last deploy:", "-")
	contains(t, mustRun(t, f, "containers", "credentials", "apps"), "admin", "pw-123")
	contains(t, mustRun(t, f, "containers", "plans"), "PLAN", "s-1vcpu-2gb", "1299", "2 GB")
	if r := run(t, f, "", "containers", "create", "--name", "x"); r.code != 2 || !strings.Contains(r.err, "--plan are required") {
		t.Errorf("create without plan: %+v", r)
	}
}

func TestContainersDeployFollowsOutput(t *testing.T) {
	f := newFakeAPI(t)
	id := newHost(t, f)

	r := run(t, f, compose, "containers", "deploy", "apps", "-f", "-", "--note", "abc123", "--wait")
	if r.code != 0 {
		t.Fatalf("deploy: %+v", r)
	}
	contains(t, r.err, "Deployment #1 started", "Pulling web", "Started app-web-1", "Deployment #1 succeeded")
	if strings.Count(r.err, "Pulling web") != 1 {
		t.Errorf("output repeated:\n%s", r.err)
	}
	contains(t, r.out, "#1", "succeeded", "abc123")
	dep := f.mcs.deployments[id][0]
	if dep["compose"] != compose || dep["note"] != "abc123" {
		t.Errorf("deployment %v", dep)
	}

	// a failing deployment: its output, then exit 1
	r = run(t, f, "services:\n  web:\n    image: nope\n", "containers", "deploy", "apps", "-f", "-", "--wait")
	if r.code != 1 {
		t.Fatalf("failing deploy: %+v", r)
	}
	contains(t, r.err, "pull access denied for nope", "deployment failed: #2", "redeploy")

	// the history, and a rollback to #1
	contains(t, mustRun(t, f, "containers", "deployments", "apps"), "#", "failed", "succeeded")
	r = run(t, f, "", "containers", "redeploy", "apps", "#1", "--wait")
	if r.code != 0 {
		t.Fatalf("redeploy: %+v", r)
	}
	contains(t, r.out, "#3", "succeeded", "Redeploy of #1")
	if got := f.mcs.deployments[id][2]["compose"]; got != compose {
		t.Errorf("redeployed %v", got)
	}

	// without -f: the saved file again, not waited for
	r = run(t, f, "", "containers", "deploy", "apps", "-o", "json")
	if r.code != 0 {
		t.Fatalf("deploy again: %+v", r)
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(r.out), &d); err != nil || d["status"] != "running" || d["number"] != 4.0 {
		t.Errorf("deploy -o json: %v %v", d, err)
	}
}

func TestContainersDeployWithEnvFile(t *testing.T) {
	f := newFakeAPI(t)
	id := newHost(t, f)
	envFile := writeFile(t, "DB_URL=postgres://db\nMODE=prod\n")
	composeFile := writeFile(t, compose)
	mustRun(t, f, "containers", "deploy", "apps", "-f", composeFile, "--env-file", envFile, "--wait")
	if env := f.mcs.env[id]; env["DB_URL"] != "postgres://db" || env["MODE"] != "prod" {
		t.Errorf("env %v", env)
	}
	if keys := f.mcs.deployments[id][0]["env_keys"].([]string); len(keys) != 2 {
		t.Errorf("deployed with %v", keys)
	}
}

func TestContainersEnv(t *testing.T) {
	f := newFakeAPI(t)
	id := newHost(t, f)

	// no compose file yet: saved only
	contains(t, mustRun(t, f, "containers", "env", "set", "apps", "A=1", "B=two=2"), "nothing was deployed")
	if env := f.mcs.env[id]; env["A"] != "1" || env["B"] != "two=2" {
		t.Fatalf("env %v", env)
	}
	mustRun(t, f, "containers", "deploy", "apps", "-f", writeFile(t, compose), "--wait")

	// with one: set deploys and keeps the others
	r := run(t, f, "", "containers", "env", "set", "apps", "C=3", "--wait")
	if r.code != 0 {
		t.Fatalf("env set: %+v", r)
	}
	contains(t, r.err, "Deployment #2 succeeded")
	if env := f.mcs.env[id]; len(env) != 3 || env["A"] != "1" {
		t.Errorf("env %v", env)
	}
	contains(t, mustRun(t, f, "containers", "env", "get", "apps"), "A=1\n", "B=two=2\n", "C=3\n")
	if got := mustRun(t, f, "containers", "env", "get", "apps", "B"); got != "two=2\n" {
		t.Errorf("env get B = %q", got)
	}

	contains(t, mustRun(t, f, "containers", "env", "unset", "apps", "A", "--no-deploy"), "take effect with the next deployment")
	if _, ok := f.mcs.env[id]["A"]; ok || len(f.mcs.deployments[id]) != 2 {
		t.Errorf("env %v, %d deployments", f.mcs.env[id], len(f.mcs.deployments[id]))
	}
	if r := run(t, f, "", "containers", "env", "unset", "apps", "NOPE"); r.code != 1 || !strings.Contains(r.err, "no environment variable NOPE") {
		t.Errorf("unset unknown: %+v", r)
	}
	if r := run(t, f, "", "containers", "env", "set", "apps", "novalue"); r.code != 2 {
		t.Errorf("set without =: %+v", r)
	}
	mustRun(t, f, "containers", "env", "set", "apps", "--from-file", writeFile(t, "# comment\nexport D=\"quoted\"\n"), "--no-deploy")
	if f.mcs.env[id]["D"] != "quoted" {
		t.Errorf("env %v", f.mcs.env[id])
	}
}

func TestContainersDomains(t *testing.T) {
	f := newFakeAPI(t)
	id := newHost(t, f)
	mustRun(t, f, "containers", "deploy", "apps", "-f", writeFile(t, compose), "--wait")

	r := run(t, f, "", "containers", "domains", "add", "apps", "App.Example.com", "--service", "web", "--port", "8080", "--wait")
	if r.code != 0 {
		t.Fatalf("domains add: %+v", r)
	}
	contains(t, r.out, "app.example.com")
	mustRun(t, f, "containers", "domains", "add", "apps", "api.example.com", "--service", "web", "--port", "9000", "--no-deploy")
	contains(t, mustRun(t, f, "containers", "domains", "list", "apps"), "DOMAIN", "app.example.com", "8080", "api.example.com", "active")

	// adding a domain again moves it
	mustRun(t, f, "containers", "domains", "add", "apps", "app.example.com", "--service", "web", "--port", "8081", "--no-deploy")
	if ds := f.store["container-hosts"][id]["domains"].([]any); len(ds) != 2 {
		t.Fatalf("domains %v", ds)
	}

	mustRun(t, f, "containers", "domains", "remove", "apps", "api.example.com", "--no-deploy")
	ds := f.store["container-hosts"][id]["domains"].([]any)
	if len(ds) != 1 || ds[0].(map[string]any)["port"] != 8081.0 {
		t.Fatalf("domains %v", ds)
	}
	if r := run(t, f, "", "containers", "domains", "remove", "apps", "nope.example.com"); r.code != 1 || !strings.Contains(r.err, "does not serve nope.example.com") {
		t.Errorf("remove unknown: %+v", r)
	}
	if r := run(t, f, "", "containers", "domains", "add", "apps", "x.example.com"); r.code != 2 {
		t.Errorf("add without service: %+v", r)
	}
}

func TestContainersLogsAndActions(t *testing.T) {
	f := newFakeAPI(t)
	newHost(t, f)
	contains(t, mustRun(t, f, "containers", "status", "apps"), "app-web-1", "web", "(system)", "0.10%")

	// a compose service names its container
	if got := mustRun(t, f, "containers", "logs", "apps", "web", "-n", "50"); got != "2026-11-20T10:00:00Z app-web-1 says hello\n" {
		t.Errorf("logs = %q", got)
	}
	if f.mcs.logLines["app-web-1"] != "50" {
		t.Errorf("lines %v", f.mcs.logLines)
	}
	contains(t, mustRun(t, f, "containers", "restart", "apps", "app-web-1"), "restart app-web-1: done")
	mustRun(t, f, "containers", "stop", "apps", "web")
	mustRun(t, f, "containers", "start", "apps", "web")
	if got := strings.Join(f.mcs.actions, ";"); got != "restart app-web-1;stop app-web-1;start app-web-1" {
		t.Errorf("actions %s", got)
	}
	if r := run(t, f, "", "containers", "stop", "apps", "traefik"); r.code != 1 || !strings.Contains(r.err, "restart it instead. (conflict)") {
		t.Errorf("stop traefik: %+v", r)
	}
	if r := run(t, f, "", "containers", "logs", "apps", "db"); r.code != 1 || !strings.Contains(r.err, `no container or compose service named "db"`) {
		t.Errorf("logs db: %+v", r)
	}
}

func TestContainersPlanAndDelete(t *testing.T) {
	f := newFakeAPI(t)
	id := newHost(t, f)
	r := run(t, f, "", "containers", "plan", "apps", "s-4vcpu-8gb", "--wait")
	if r.code != 0 {
		t.Fatalf("plan: %+v", r)
	}
	contains(t, r.out, "s-4vcpu-8gb")
	if f.store["container-hosts"][id]["plan"] != "s-4vcpu-8gb" {
		t.Errorf("plan not changed")
	}

	if r := run(t, f, "", "containers", "delete", "apps"); r.code == 0 || len(f.list("container-hosts")) != 1 {
		t.Fatalf("delete without --yes: %+v", r)
	}
	r = run(t, f, "", "containers", "delete", "apps", "--yes", "--wait")
	if r.code != 0 || len(f.list("container-hosts")) != 0 {
		t.Fatalf("delete: %+v", r)
	}
	contains(t, r.out, "Deleted container host apps")
	if r := run(t, f, "", "containers", "get", "apps"); r.code != 1 || !strings.Contains(r.err, `no container host with ID or name "apps"`) {
		t.Errorf("get after delete: %+v", r)
	}
}

func TestContainersNotReady(t *testing.T) {
	f := newFakeAPI(t)
	mustRun(t, f, "containers", "create", "--name", "apps", "--plan", "s-2vcpu-4gb")
	r := run(t, f, compose, "containers", "deploy", "apps", "-f", "-")
	if r.code != 1 || !strings.Contains(r.err, "The host is not ready yet (conflict)") {
		t.Errorf("deploy before ready: %+v", r)
	}
	// domains list falls back to what is saved
	contains(t, mustRun(t, f, "containers", "domains", "list", "apps"), "DOMAIN")
	contains(t, mustRun(t, f, "containers", "--help"), "deploy", "domains", "env", "billed per hour")
}

func TestNewOutput(t *testing.T) {
	for _, c := range []struct{ shown, out, want string }{
		{"", "a\n", "a\n"},
		{"a\n", "a\nb\n", "b\n"},
		{"a\nb\n", "b\nc\n", "c\n"}, // the start was cut off
		{"a\n", "x\ny", "x\ny\n"},   // nothing in common
		{"a\nb\n", "a\nb\n", ""},
	} {
		if got := newOutput(c.shown, c.out); got != c.want {
			t.Errorf("newOutput(%q, %q) = %q, want %q", c.shown, c.out, got, c.want)
		}
	}
}
