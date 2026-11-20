package cli

import (
	"fmt"
	"net/http"
	"strings"
)

// fakeMCS is the container host part of the fake API: hosts are ready on the second look,
// deployments finish on the second look (they fail when the compose file mentions "image: nope"),
// and live calls need a ready host.
type fakeMCS struct {
	env         map[string]map[string]string
	deployments map[string][]map[string]any // host id -> deployments, oldest first
	logLines    map[string]string           // ?lines= of the last logs call, by container
	actions     []string                    // "action container"
}

func newFakeMCS() *fakeMCS {
	return &fakeMCS{env: map[string]map[string]string{}, deployments: map[string][]map[string]any{}, logLines: map[string]string{}}
}

func (f *fakeAPI) containerHosts(w http.ResponseWriter, r *http.Request, p []string, body map[string]any) {
	m := f.mcs
	if len(p) == 2 && p[1] == "options" {
		reply(w, 200, map[string]any{"enabled": true, "plans": []map[string]any{
			{"slug": "s-1vcpu-2gb", "cpu": 1, "memory_gb": 2.0, "disk_gb": 50, "server_per_month": 999, "fee_per_month": 300, "per_month": 1299, "per_hour": 1.8},
			{"slug": "s-2vcpu-4gb", "cpu": 2, "memory_gb": 4.0, "disk_gb": 80, "server_per_month": 1799, "fee_per_month": 400, "per_month": 2199, "per_hour": 3.05},
		}})
		return
	}
	last := func(id string) any {
		deps := m.deployments[id]
		if len(deps) == 0 {
			return nil
		}
		return deps[len(deps)-1]
	}
	if len(p) == 1 {
		switch r.Method {
		case "GET":
			out := []map[string]any{}
			for _, h := range f.list("container-hosts") {
				if pid := r.URL.Query().Get("project_id"); pid != "" && h["project_id"] != pid {
					continue
				}
				h["last_deployment"] = last(h["id"].(string))
				out = append(out, h)
			}
			reply(w, 200, out)
		case "POST":
			project, _ := body["project_id"].(string)
			if project == "" {
				project = "p1"
			}
			h := f.put("container-hosts", map[string]any{"name": body["name"], "plan": body["plan"], "status": "creating", "project_id": project,
				"server":        map[string]any{"status": "creating", "ip_address": "203.0.113.40", "region": "dhaka-1"},
				"portainer_url": "https://203-0-113-40.sslip.io", "domains": []any{}, "compose": nil,
				"price": map[string]any{"server_per_month": 1799, "fee_per_month": 400, "per_month": 2199}})
			m.env[h["id"].(string)] = map[string]string{}
			reply(w, 201, h)
		}
		return
	}
	id := p[1]
	h, ok := f.store["container-hosts"][id]
	if !ok {
		reply(w, 404, map[string]any{"detail": "Container host not found", "code": "not_found"})
		return
	}
	server := h["server"].(map[string]any)
	ready := func() bool {
		if h["status"] != "ready" {
			reply(w, 409, map[string]any{"detail": "The host is not ready yet", "code": "conflict"})
			return false
		}
		return true
	}
	deploy := func(compose any, domains any, note any) map[string]any {
		if compose == nil || compose == "" {
			compose = h["compose"]
		}
		if domains == nil {
			domains = h["domains"]
		}
		h["compose"], h["domains"] = compose, domains
		keys := []string{}
		for k := range m.env[id] {
			keys = append(keys, k)
		}
		f.seq++
		dep := map[string]any{"id": fmt.Sprintf("dep-%d", f.seq), "number": len(m.deployments[id]) + 1, "status": "running", "note": note,
			"env_keys": keys, "domains": domains, "compose": compose, "output": "Pulling web\n", "created_by": "dev@example.com"}
		m.deployments[id] = append(m.deployments[id], dep)
		f.polls[dep["id"].(string)] = 0
		return dep
	}
	switch {
	case len(p) == 2 && r.Method == "GET":
		f.polls[id]++
		if f.polls[id] >= 2 && h["status"] == "creating" {
			h["status"], server["status"] = "ready", "running"
		}
		if f.polls[id] >= 2 && h["status"] == "deleting" {
			delete(f.store["container-hosts"], id)
			reply(w, 404, map[string]any{"detail": "Container host not found", "code": "not_found"})
			return
		}
		if f.polls[id] >= 2 && server["status"] == "resizing" {
			h["plan"], server["status"] = h["resize"].(map[string]any)["to"], "running"
		}
		out := map[string]any{}
		for k, v := range h {
			out[k] = v
		}
		keys := []string{}
		for k := range m.env[id] {
			keys = append(keys, k)
		}
		out["env_keys"], out["last_deployment"] = keys, last(id)
		reply(w, 200, out)
	case len(p) == 2 && r.Method == "DELETE":
		h["status"] = "deleting"
		f.polls[id] = 0
		reply(w, 200, map[string]any{"message": "Deleting the host and its server"})
	case p[2] == "credentials":
		if ready() {
			reply(w, 200, map[string]any{"portainer_url": h["portainer_url"], "username": "admin", "password": "pw-123", "note": "first password"})
		}
	case p[2] == "plan":
		if ready() {
			server["status"], h["resize"] = "resizing", map[string]any{"to": body["plan"], "error": nil}
			f.polls[id] = 0
			reply(w, 202, h)
		}
	case p[2] == "status":
		if !ready() {
			return
		}
		domains := []map[string]any{}
		for _, raw := range h["domains"].([]any) {
			d := raw.(map[string]any)
			domains = append(domains, map[string]any{"domain": d["domain"], "service": d["service"], "port": d["port"],
				"dns": []string{"203.0.113.40"}, "points_here": true, "ssl": "active"})
		}
		reply(w, 200, map[string]any{
			"containers": []map[string]any{
				{"id": "a1", "name": "app-web-1", "image": "nginx", "state": "running", "status": "Up 1 hour", "service": "web", "system": false,
					"stats": map[string]any{"cpu": "0.10%", "memory": "12MiB / 2GiB"}},
				{"id": "a2", "name": "traefik", "image": "traefik:v3.6", "state": "running", "status": "Up 2 hours", "service": "traefik", "system": true},
			},
			"disk": map[string]any{"total": 50, "used": 10, "free": 40}, "certificates": []string{}, "domains": domains,
		})
	case p[2] == "containers" && p[4] == "logs":
		if ready() {
			m.logLines[p[3]] = r.URL.Query().Get("lines")
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintf(w, "2026-11-20T10:00:00Z %s says hello\n", p[3])
		}
	case p[2] == "containers" && p[4] == "action":
		if !ready() {
			return
		}
		if p[3] == "traefik" && body["action"] == "stop" {
			reply(w, 409, map[string]any{"detail": "This container runs the reverse proxy or Portainer; restart it instead.", "code": "conflict"})
			return
		}
		m.actions = append(m.actions, fmt.Sprintf("%v %s", body["action"], p[3]))
		reply(w, 200, map[string]any{"message": "done"})
	case p[2] == "deploy":
		if !ready() {
			return
		}
		if body["compose"] == nil && h["compose"] == nil {
			reply(w, 422, map[string]any{"detail": "Add a compose file first", "code": "validation_error"})
			return
		}
		reply(w, 202, deploy(body["compose"], nil, body["note"]))
	case p[2] == "env" && r.Method == "GET":
		reply(w, 200, map[string]any{"env": m.env[id]})
	case p[2] == "env":
		env := map[string]string{}
		if text, ok := body["text"].(string); ok {
			for _, line := range strings.Split(text, "\n") {
				if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
					env[k] = v
				}
			}
		} else {
			for k, v := range body["env"].(map[string]any) {
				env[k] = v.(string)
			}
		}
		m.env[id] = env
		keys := []string{}
		for k := range env {
			keys = append(keys, k)
		}
		var dep any
		if body["deploy"] == true && h["compose"] != nil && ready() {
			dep = deploy(nil, nil, "Environment variables changed")
		}
		reply(w, 200, map[string]any{"env_keys": keys, "deployment": dep})
	case p[2] == "domains":
		var dep any
		if body["deploy"] == true && h["compose"] != nil && ready() {
			dep = deploy(nil, body["domains"], "Domains changed")
		} else {
			h["domains"] = body["domains"]
		}
		reply(w, 200, map[string]any{"domains": h["domains"], "deployment": dep})
	case p[2] == "deployments" && len(p) == 3:
		out := []map[string]any{}
		for i := len(m.deployments[id]) - 1; i >= 0; i-- {
			out = append(out, m.deployments[id][i])
		}
		reply(w, 200, out)
	case p[2] == "deployments" && len(p) == 4:
		for _, d := range m.deployments[id] {
			if d["id"] != p[3] {
				continue
			}
			did := d["id"].(string)
			f.polls[did]++
			if d["status"] == "running" && f.polls[did] >= 2 {
				if strings.Contains(fmt.Sprint(d["compose"]), "image: nope") {
					d["status"], d["output"] = "failed", "Pulling web\nError: pull access denied for nope\n"
				} else {
					d["status"], d["output"] = "succeeded", "Pulling web\nStarted app-web-1\n"
				}
			}
			reply(w, 200, d)
			return
		}
		reply(w, 404, map[string]any{"detail": "Deployment not found", "code": "not_found"})
	case p[2] == "deployments" && len(p) == 5 && p[4] == "redeploy":
		if !ready() {
			return
		}
		for _, d := range m.deployments[id] {
			if d["id"] == p[3] {
				reply(w, 202, deploy(d["compose"], d["domains"], fmt.Sprintf("Redeploy of #%v", d["number"])))
				return
			}
		}
		reply(w, 404, map[string]any{"detail": "Deployment not found", "code": "not_found"})
	default:
		notFound(w)
	}
}
