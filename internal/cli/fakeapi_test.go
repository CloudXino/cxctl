package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAPI is an in-memory CloudXino API with just enough behaviour for the CLI: resources by kind,
// servers that finish creating / deleting on the second look, and a log of the requests made.
type fakeAPI struct {
	mu      sync.Mutex
	seq     int
	store   map[string]map[string]map[string]any // kind -> id -> object
	order   map[string][]string                  // kind -> ids in creation order
	polls   map[string]int
	log     []string // "METHOD /path"
	srv     *httptest.Server
	token   string // the key requests must carry
	ripsOff bool
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{store: map[string]map[string]map[string]any{}, order: map[string][]string{}, polls: map[string]int{}, token: "cxk_test"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	t.Setenv("CLOUDXINO_TOKEN", "")
	t.Setenv("CLOUDXINO_API_URL", "")
	t.Setenv("CXCTL_CONFIG", t.TempDir()+"/config.json")
	old := defaultWaitInterval
	defaultWaitInterval = time.Millisecond
	t.Cleanup(func() { defaultWaitInterval = old })
	f.put("projects", map[string]any{"id": "p1", "name": "Default", "is_default": true})
	f.put("projects", map[string]any{"id": "p2", "name": "Staging", "is_default": false})
	return f
}

func (f *fakeAPI) url() string { return f.srv.URL + "/api" }

func (f *fakeAPI) put(kind string, obj map[string]any) map[string]any {
	if f.store[kind] == nil {
		f.store[kind] = map[string]map[string]any{}
	}
	id, _ := obj["id"].(string)
	if id == "" {
		f.seq++
		id = fmt.Sprintf("%s-%d", strings.TrimSuffix(kind, "s"), f.seq)
		obj["id"] = id
	}
	if _, ok := f.store[kind][id]; !ok {
		f.order[kind] = append(f.order[kind], id)
	}
	f.store[kind][id] = obj
	return obj
}

func (f *fakeAPI) list(kind string) []map[string]any {
	out := []map[string]any{}
	for _, id := range f.order[kind] {
		if o, ok := f.store[kind][id]; ok {
			out = append(out, o)
		}
	}
	return out
}

func (f *fakeAPI) requests(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, l := range f.log {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter) {
	reply(w, 404, map[string]any{"detail": "Not found", "code": "not_found"})
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/api"))
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		reply(w, 401, map[string]any{"detail": "Invalid API key", "code": "unauthorized"})
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	p := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/")
	switch {
	case p[0] == "users" && p[1] == "me":
		reply(w, 200, map[string]any{"id": "u1", "email": "dev@example.com", "name": "Dev",
			"organization": map[string]any{"id": "o1", "name": "Acme", "role": "owner"},
			"api_key":      map[string]any{"name": "ci", "key_prefix": "cxk_ab12", "role": "developer", "role_label": "Developer"}})
	case p[0] == "mc2" && len(p) == 3 && p[1] == "options":
		f.options(w, p[2])
	case p[0] == "mc2":
		f.servers(w, r, p, body)
	case p[0] == "dns":
		f.dns(w, r, p[1:], body)
	case p[0] == "load-balancers" && len(p) >= 3 && p[2] == "targets":
		lb := f.store["load-balancers"][p[1]]
		ids := lb["target_ids"].([]any)
		if r.Method == "POST" {
			ids = append(ids, body["server_id"])
		} else {
			kept := []any{}
			for _, id := range ids {
				if id != p[3] {
					kept = append(kept, id)
				}
			}
			ids = kept
		}
		lb["target_ids"] = ids
		reply(w, 200, lb)
	case p[0] == "reserved-ips":
		f.reservedIPs(w, r, p, body)
	default:
		f.crud(w, r, p[0], p[1:], body)
	}
}

// reservedIPs: f.ripsOff makes every create answer 503 unavailable, as when no address is free.
func (f *fakeAPI) reservedIPs(w http.ResponseWriter, r *http.Request, p []string, body map[string]any) {
	point := func(rip map[string]any, serverID any) {
		srv := f.store["mc2"][serverID.(string)]
		rip["server_id"], rip["target_id"], rip["target_type"], rip["assigned_to"], rip["status"] = serverID, serverID, "mc2", srv["name"], "assigned"
	}
	switch {
	case len(p) == 1 && r.Method == "GET":
		out := []map[string]any{}
		for _, rip := range f.list("reserved-ips") {
			if a := r.URL.Query().Get("ip_address"); a == "" || rip["ip_address"] == a {
				out = append(out, rip)
			}
		}
		reply(w, 200, out)
	case len(p) == 1 && r.Method == "POST":
		if f.ripsOff {
			reply(w, 503, map[string]any{"detail": "Reserved IPs are not available right now", "code": "unavailable"})
			return
		}
		region, _ := body["region"].(string)
		if region == "" {
			region = "dhaka-1"
		}
		rip := f.put("reserved-ips", map[string]any{"ip_address": fmt.Sprintf("198.51.100.%d", f.seq+1), "region": region,
			"project_id": body["project_id"], "status": "available"})
		if body["server_id"] != nil {
			point(rip, body["server_id"])
		}
		reply(w, 201, rip)
	case len(p) == 3 && p[2] == "assign":
		rip := f.store["reserved-ips"][p[1]]
		point(rip, body["server_id"])
		reply(w, 200, rip)
	case len(p) == 3 && p[2] == "unassign":
		rip := f.store["reserved-ips"][p[1]]
		rip["server_id"], rip["target_id"], rip["target_type"], rip["assigned_to"], rip["status"] = nil, nil, nil, nil, "available"
		reply(w, 200, rip)
	default:
		f.crud(w, r, p[0], p[1:], body)
	}
}

// crud is list / get / create / patch / delete for a plain kind.
func (f *fakeAPI) crud(w http.ResponseWriter, r *http.Request, kind string, rest []string, body map[string]any) {
	if len(rest) == 0 {
		switch r.Method {
		case "GET":
			reply(w, 200, f.list(kind))
		case "POST":
			obj := map[string]any{}
			for k, v := range body {
				obj[k] = v
			}
			switch kind {
			case "ssh-keys":
				obj["fingerprint"] = "SHA256:" + strings.Fields(body["public_key"].(string))[1][:6]
			case "firewalls":
				obj["status"], obj["server_count"] = "active", 0
			case "load-balancers":
				obj["status"], obj["ip_address"], obj["region"] = "creating", "203.0.113.50", "dhaka-1"
				if obj["target_ids"] == nil {
					obj["target_ids"] = []any{}
				}
			}
			reply(w, 201, f.put(kind, obj))
		}
		return
	}
	obj, ok := f.store[kind][rest[0]]
	if !ok {
		notFound(w)
		return
	}
	switch r.Method {
	case "GET":
		if kind == "load-balancers" {
			obj["status"] = "active"
		}
		reply(w, 200, obj)
	case "PATCH":
		for k, v := range body {
			obj[k] = v
		}
		reply(w, 200, obj)
	case "DELETE":
		delete(f.store[kind], rest[0])
		w.WriteHeader(204)
	}
}

func (f *fakeAPI) options(w http.ResponseWriter, what string) {
	switch what {
	case "sizes":
		reply(w, 200, []map[string]any{{"slug": "s-1vcpu-1gb", "name": "1 vCPU, 1 GB RAM", "cpu": 1, "memory": 1, "disk": 25,
			"price_per_hour": 0.5, "price_per_month": 360, "public_price_per_hour": 0.8, "public_price_per_month": 576}})
	case "regions":
		reply(w, 200, []map[string]any{{"id": "dhaka-1", "name": "dhaka-1", "display_name": "Dhaka 1", "location": "Dhaka"}})
	case "images":
		reply(w, 200, []map[string]any{{"id": "debian-13", "name": "Debian 13", "login_user": "root"}})
	default:
		notFound(w)
	}
}

func (f *fakeAPI) servers(w http.ResponseWriter, r *http.Request, p []string, body map[string]any) {
	if len(p) == 1 && r.Method == "POST" {
		if body["size"] == "nope" {
			reply(w, 422, map[string]any{"detail": "Unknown size 'nope'", "code": "validation_error"})
			return
		}
		obj := map[string]any{"status": "creating", "region": "dhaka-1", "project_id": "p1", "progress": map[string]any{"percent": 10}}
		for k, v := range body {
			obj[k] = v
		}
		reply(w, 201, f.put("mc2", obj))
		return
	}
	if len(p) == 1 {
		f.crud(w, r, "mc2", nil, body)
		return
	}
	s, ok := f.store["mc2"][p[1]]
	if !ok {
		notFound(w)
		return
	}
	if len(p) == 3 {
		switch p[2] {
		case "ssh-key":
			w.Write([]byte("-----BEGIN OPENSSH PRIVATE KEY-----\nfake\n-----END OPENSSH PRIVATE KEY-----\n"))
		case "power-off":
			s["status"] = "stopped"
			reply(w, 200, map[string]any{"message": "Powering off", "status": "stopping"})
		case "power-on", "reboot":
			s["status"] = "running"
			reply(w, 200, map[string]any{"message": "OK", "status": "starting"})
		case "resize":
			s["status"], s["size"] = "resizing", body["size"]
			f.polls[p[1]] = 0
			reply(w, 200, map[string]any{"message": "Resize started", "status": "resizing"})
		}
		return
	}
	switch r.Method {
	case "GET":
		f.polls[p[1]]++
		if f.polls[p[1]] >= 2 {
			switch s["status"] {
			case "creating", "resizing":
				s["status"], s["ip_address"], s["progress"] = "running", "203.0.113.20", nil
			case "deleting":
				delete(f.store["mc2"], p[1])
				notFound(w)
				return
			}
		}
		reply(w, 200, s)
	case "DELETE":
		s["status"] = "deleting"
		f.polls[p[1]] = 0
		reply(w, 200, map[string]any{"message": "deletion started"})
	default:
		f.crud(w, r, "mc2", p[1:], body)
	}
}

func (f *fakeAPI) dns(w http.ResponseWriter, r *http.Request, p []string, body map[string]any) {
	// p: zones[/id[/records[/rid]|/export|/import]]
	if len(p) == 1 {
		if r.Method == "POST" {
			z := f.put("zones", map[string]any{"name": body["name"], "status": "pending", "nameservers": []string{"ns1.example.net", "ns2.example.net"}})
			reply(w, 201, z)
			return
		}
		reply(w, 200, f.list("zones"))
		return
	}
	z, ok := f.store["zones"][p[1]]
	if !ok {
		notFound(w)
		return
	}
	view := func(rec map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range z {
			out[k] = v
		}
		recs := []map[string]any{}
		for _, rr := range f.list("records") {
			if rr["zone_id"] == z["id"] {
				recs = append(recs, rr)
			}
		}
		out["records"], out["record_count"] = recs, len(recs)
		if rec != nil {
			out["record"] = rec
		}
		return out
	}
	if len(p) == 2 {
		if r.Method == "DELETE" {
			delete(f.store["zones"], p[1])
			w.WriteHeader(204)
			return
		}
		reply(w, 200, view(nil))
		return
	}
	switch p[2] {
	case "export":
		w.Write([]byte("$ORIGIN " + z["name"].(string) + ".\n"))
		return
	case "import":
		f.put("records", map[string]any{"zone_id": z["id"], "name": "imported", "type": "TXT", "content": body["text"], "ttl": 3600})
		reply(w, 200, map[string]any{"message": "imported"})
		return
	}
	if len(p) == 3 && r.Method == "POST" {
		rec := map[string]any{"zone_id": z["id"]}
		for k, v := range body {
			rec[k] = v
		}
		if rec["ttl"] == nil {
			rec["ttl"] = 3600
		}
		rec["fqdn"] = strings.TrimPrefix(rec["name"].(string)+".", "@.") + z["name"].(string)
		reply(w, 201, view(f.put("records", rec)))
		return
	}
	rec, ok := f.store["records"][p[3]]
	if !ok {
		notFound(w)
		return
	}
	switch r.Method {
	case "PATCH":
		for k, v := range body {
			rec[k] = v
		}
		reply(w, 200, view(rec))
	case "DELETE":
		delete(f.store["records"], p[3])
		reply(w, 200, view(nil))
	default:
		reply(w, 200, rec)
	}
}
