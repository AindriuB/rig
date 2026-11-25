package jira

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// token is the credential every stub accepts.
const token = "TOKEN-9d41c7"

// stub replays the recorded responses in testdata for one dialect, and keeps
// the state a contract run changes: status, updated, comments. It never
// reaches the network.
type stub struct {
	t       *testing.T
	srv     *httptest.Server
	version int // 2 or 3
	user    string
	pageCap int // most issues per search page; 0 means as asked

	mu          sync.Mutex
	issues      map[string]map[string]any
	order       []string
	transitions map[string][]map[string]any
	reqs        []string // "METHOD path?query"
	auths       []string // Authorization header of each request
	comments    []map[string]any
	posted      int              // POST requests other than comments
	created     []map[string]any // bodies of POST /issue
	tick        int
}

func readJSON(t *testing.T, rel string, dst any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
}

func newStub(t *testing.T, version int) *stub {
	t.Helper()
	s := &stub{t: t, version: version, issues: map[string]map[string]any{}}
	if version == 3 {
		s.user = "ann@acme.io"
	}
	var list []map[string]any
	readJSON(t, fmt.Sprintf("v%d/issues.json", version), &list)
	for _, is := range list {
		key := is["key"].(string)
		s.issues[key] = is
		s.order = append(s.order, key)
	}
	readJSON(t, "transitions.json", &s.transitions)
	s.srv = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stub) config() Config {
	api := "v" + strconv.Itoa(s.version)
	return Config{Site: s.srv.URL, User: s.user, Token: token, API: api, Project: "RIG", HTTPClient: s.srv.Client()}
}

// paths returns the recorded request paths without their query.
func (s *stub) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.reqs {
		_, uri, _ := strings.Cut(r, " ")
		out = append(out, strings.SplitN(uri, "?", 2)[0])
	}
	return out
}

func (s *stub) count(prefix string) int {
	n := 0
	for _, p := range s.paths() {
		if strings.HasPrefix(p, prefix) {
			n++
		}
	}
	return n
}

func (s *stub) wantAuth() string {
	if s.user != "" {
		r, _ := http.NewRequest("GET", "/", nil)
		r.SetBasicAuth(s.user, token)
		return r.Header.Get("Authorization")
	}
	return "Bearer " + token
}

func (s *stub) reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		json.NewEncoder(w).Encode(v)
	}
}

func (s *stub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, r.Method+" "+r.URL.RequestURI())
	s.auths = append(s.auths, r.Header.Get("Authorization"))
	if r.Header.Get("Authorization") != s.wantAuth() {
		s.reply(w, 401, map[string]any{"errorMessages": []string{"unauthorized"}})
		return
	}
	prefix := fmt.Sprintf("/rest/api/%d", s.version)
	path := r.URL.Path
	switch {
	case r.Method == "GET" && path == "/rest/api/2/serverInfo":
		var v any
		readJSON(s.t, fmt.Sprintf("v%d/serverInfo.json", s.version), &v)
		s.reply(w, 200, v)
	case r.Method == "GET" && path == "/rest/api/2/field":
		var v any
		readJSON(s.t, "v2/field.json", &v)
		s.reply(w, 200, v)
	case r.Method == "GET" && path == prefix+"/myself":
		var v any
		readJSON(s.t, "myself.json", &v)
		s.reply(w, 200, v)
	case r.Method == "GET" && strings.HasPrefix(path, prefix+"/project/"):
		key := strings.TrimPrefix(path, prefix+"/project/")
		if key != "RIG" && key != "EMPTY" {
			s.reply(w, 404, map[string]any{"errorMessages": []string{"No project could be found with key '" + key + "'."}})
			return
		}
		s.reply(w, 200, map[string]any{"key": key})
	case r.Method == "GET" && (path == prefix+"/search" || path == prefix+"/search/jql"):
		s.search(w, r)
	case r.Method == "GET" && strings.HasPrefix(path, prefix+"/issue/createmeta/"):
		s.createmeta(w)
	case r.Method == "POST" && path == prefix+"/issue":
		s.create(w, r)
	case strings.HasPrefix(path, prefix+"/issue/"):
		s.issue(w, r, strings.TrimPrefix(path, prefix+"/issue/"))
	default:
		s.reply(w, 404, map[string]any{"errorMessages": []string{"no such endpoint " + path}})
	}
}

func (s *stub) issue(w http.ResponseWriter, r *http.Request, rest string) {
	key, sub, _ := strings.Cut(rest, "/")
	is, ok := s.issues[key]
	if !ok {
		s.reply(w, 404, map[string]any{"errorMessages": []string{"Issue does not exist or you do not have permission to see it."}})
		return
	}
	fields := is["fields"].(map[string]any)
	status := fields["status"].(map[string]any)["name"].(string)
	switch {
	case r.Method == "GET" && sub == "":
		out := map[string]any{"key": key, "id": is["id"], "fields": map[string]any{}}
		want := strings.Split(r.URL.Query().Get("fields"), ",")
		for _, f := range want {
			if v, ok := fields[f]; ok {
				out["fields"].(map[string]any)[f] = v
			}
		}
		s.reply(w, 200, out)
	case r.Method == "GET" && sub == "transitions":
		s.reply(w, 200, map[string]any{"expand": "transitions", "transitions": s.transitions[status]})
	case r.Method == "POST" && sub == "transitions":
		s.posted++
		var body struct {
			Transition struct {
				ID string `json:"id"`
			} `json:"transition"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			s.reply(w, 400, nil)
			return
		}
		for _, tr := range s.transitions[status] {
			if tr["id"] == body.Transition.ID {
				fields["status"] = tr["to"]
				s.tick++
				fields["updated"] = fmt.Sprintf("2025-04-01T10:00:%02d.000+0000", s.tick)
				s.reply(w, 204, nil)
				return
			}
		}
		s.reply(w, 400, map[string]any{"errorMessages": []string{"Transition is not valid"}})
	case r.Method == "POST" && sub == "comment":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			s.reply(w, 400, nil)
			return
		}
		s.comments = append(s.comments, body)
		s.reply(w, 201, map[string]any{"id": strconv.Itoa(10000 + len(s.comments)), "body": body["body"], "created": "2025-04-01T10:00:00.000+0000"})
	default:
		s.reply(w, 404, nil)
	}
}

var (
	labelClauseRE = regexp.MustCompile(`labels = "((?:[^"\\]|\\.)*)"`)
	parentRE      = regexp.MustCompile(`^parent = (\S+)$`)
	projectJQLRE  = regexp.MustCompile(`^project = (\S+)$`)
	epicRE        = regexp.MustCompile(`^(?:"Epic Link"|cf\[10014\]) = (\S+)$`)
)

// matches evaluates the small JQL subset the adapter emits: AND-ed clauses.
func (s *stub) matches(is map[string]any, alt string) bool {
	fields := is["fields"].(map[string]any)
	for _, clause := range strings.Split(alt, " AND ") {
		clause = strings.Trim(strings.TrimSpace(clause), "()")
		switch {
		case labelClauseRE.MatchString(clause):
			want := strings.ReplaceAll(labelClauseRE.FindStringSubmatch(clause)[1], `\"`, `"`)
			found := false
			if ls, _ := fields["labels"].([]any); ls != nil {
				for _, l := range ls {
					found = found || l == want
				}
			}
			if !found {
				return false
			}
		case parentRE.MatchString(clause):
			p, _ := fields["parent"].(map[string]any)
			if p == nil || p["key"] != parentRE.FindStringSubmatch(clause)[1] {
				return false
			}
		case epicRE.MatchString(clause):
			if fields["customfield_10014"] != epicRE.FindStringSubmatch(clause)[1] {
				return false
			}
		case projectJQLRE.MatchString(clause):
			if !strings.HasPrefix(is["key"].(string), projectJQLRE.FindStringSubmatch(clause)[1]+"-") {
				return false
			}
		default:
			s.t.Errorf("stub: unsupported JQL clause %q", clause)
			return false
		}
	}
	return true
}

func (s *stub) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	jql, _, _ := strings.Cut(q.Get("jql"), " ORDER BY ")
	var hits []map[string]any
	for _, key := range s.order {
		for _, alt := range strings.Split(jql, " OR ") {
			if s.matches(s.issues[key], alt) {
				hits = append(hits, s.issues[key])
				break
			}
		}
	}
	max, _ := strconv.Atoi(q.Get("maxResults"))
	if max <= 0 || s.pageCap > 0 && max > s.pageCap {
		max = s.pageCap
	}
	start := 0
	if s.version == 3 {
		start, _ = strconv.Atoi(q.Get("nextPageToken"))
	} else {
		start, _ = strconv.Atoi(q.Get("startAt"))
	}
	end := min(len(hits), start+max)
	page := []map[string]any{}
	for _, is := range hits[min(start, len(hits)):end] {
		fields := map[string]any{}
		for _, f := range strings.Split(q.Get("fields"), ",") {
			if v, ok := is["fields"].(map[string]any)[f]; ok {
				fields[f] = v
			}
		}
		page = append(page, map[string]any{"key": is["key"], "id": is["id"], "fields": fields})
	}
	out := map[string]any{"issues": page}
	if s.version == 3 {
		out["isLast"] = end >= len(hits)
		if end < len(hits) {
			out["nextPageToken"] = strconv.Itoa(end)
		}
	} else {
		out["startAt"], out["maxResults"], out["total"] = start, max, len(hits)
	}
	s.reply(w, 200, out)
}

// createmeta answers the issue type list: hierarchyLevel on Cloud only.
func (s *stub) createmeta(w http.ResponseWriter) {
	if s.version == 3 {
		s.reply(w, 200, map[string]any{"issueTypes": []map[string]any{
			{"name": "Epic", "subtask": false, "hierarchyLevel": 1},
			{"name": "Task", "subtask": false, "hierarchyLevel": 0},
			{"name": "Sub-task", "subtask": true, "hierarchyLevel": -1},
		}})
		return
	}
	s.reply(w, 200, map[string]any{"values": []map[string]any{
		{"name": "Epic", "subtask": false},
		{"name": "Task", "subtask": false},
		{"name": "Sub-task", "subtask": true},
	}})
}

// create stores the issue a POST describes and records its body.
func (s *stub) create(w http.ResponseWriter, r *http.Request) {
	s.posted++
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.reply(w, 400, nil)
		return
	}
	s.created = append(s.created, body)
	in, _ := body["fields"].(map[string]any)
	key := fmt.Sprintf("RIG-%d", 100+len(s.created))
	fields := map[string]any{"status": map[string]any{"name": "To Do"}, "updated": "2025-04-02T10:00:00.000+0000"}
	for k, v := range in {
		fields[k] = v
	}
	if p, ok := in["parent"].(map[string]any); ok {
		fields["parent"] = map[string]any{"key": p["key"], "fields": map[string]any{}}
	}
	s.issues[key] = map[string]any{"key": key, "id": strconv.Itoa(20000 + len(s.created)), "fields": fields}
	s.order = append(s.order, key)
	s.reply(w, 201, map[string]any{"id": s.issues[key]["id"], "key": key})
}
