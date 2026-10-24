package bitbucket

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mhvelplund/git-stack/pkg/protocol"
)

const repoPath = "/repositories/ws/repo"

// fakeBitbucket answers the API calls the provider makes for ws/repo.
type fakeBitbucket struct {
	mu    sync.Mutex
	srv   *httptest.Server
	prs   map[int]map[string]any
	next  int
	calls []string         // "METHOD path"
	auth  []string         // Authorization header of each call
	puts  []map[string]any // bodies of PUTs
	// merge answers POST .../merge; nil merges at once.
	merge func(id int, body map[string]any) (int, any)
	// tasks answers GET task-status/<n> by poll count.
	tasks        map[string][]any
	restrictions any // body or status (int) of branch-restrictions
	strategy     string
}

func newFake(t *testing.T) *fakeBitbucket {
	f := &fakeBitbucket{prs: map[int]map[string]any{}, next: 1, tasks: map[string][]any{}, restrictions: map[string]any{"values": []any{}}}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func endpoint(branch, hash, repo string) map[string]any {
	return map[string]any{
		"branch": map[string]any{"name": branch}, "commit": map[string]any{"hash": hash},
		"repository": map[string]any{"full_name": repo},
	}
}

func (f *fakeBitbucket) addPR(state, head, base string, extra map[string]any) int {
	id := f.next
	f.next++
	pr := map[string]any{
		"id": id, "title": "T" + strconv.Itoa(id), "description": "", "state": state, "draft": false,
		"source": endpoint(head, "sha"+head, "ws/repo"), "destination": endpoint(base, "", "ws/repo"),
		"reviewers": []any{}, "close_source_branch": false,
		"links": map[string]any{"html": map[string]any{"href": "https://bitbucket.org/ws/repo/pull-requests/" + strconv.Itoa(id)}},
	}
	for k, v := range extra {
		pr[k] = v
	}
	f.prs[id] = pr
	return id
}

var (
	prRe     = regexp.MustCompile(`^` + repoPath + `/pullrequests/(\d+)(/merge)?$`)
	condRe   = regexp.MustCompile(`([a-z.]+) (=|~) "((?:[^"\\]|\\.)*)"`)
	branchRe = regexp.MustCompile(`^` + repoPath + `/refs/branches/(.+)$`)
)

func (f *fakeBitbucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	status, out := f.route(r, body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(out)
}

func notFound() (int, any) {
	return http.StatusNotFound, map[string]any{"type": "error", "error": map[string]any{"message": "Not found"}}
}

func (f *fakeBitbucket) route(r *http.Request, body map[string]any) (int, any) {
	path := r.URL.Path
	switch {
	case path == repoPath && r.Method == http.MethodGet:
		return http.StatusOK, map[string]any{"mainbranch": map[string]any{"name": "main"}}
	case path == repoPath+"/branch-restrictions":
		if s, ok := f.restrictions.(int); ok {
			return s, map[string]any{"error": map[string]any{"message": "forbidden"}}
		}
		return http.StatusOK, f.restrictions
	case branchRe.MatchString(path):
		return http.StatusOK, map[string]any{"name": branchRe.FindStringSubmatch(path)[1], "default_merge_strategy": f.strategy}
	case strings.HasPrefix(path, "/task-status/"):
		key := strings.TrimPrefix(path, "/task-status/")
		steps := f.tasks[key]
		if len(steps) == 0 {
			return notFound()
		}
		f.tasks[key] = steps[1:]
		return http.StatusOK, steps[0]
	case path == repoPath+"/pullrequests" && r.Method == http.MethodGet:
		return http.StatusOK, f.list(r)
	case path == repoPath+"/pullrequests" && r.Method == http.MethodPost:
		body["id"] = f.next
		body["state"] = "OPEN"
		f.prs[f.next] = body
		f.next++
		return http.StatusCreated, body
	}
	m := prRe.FindStringSubmatch(path)
	if m == nil {
		return notFound()
	}
	id, _ := strconv.Atoi(m[1])
	pr := f.prs[id]
	if pr == nil {
		return notFound()
	}
	switch {
	case m[2] == "/merge" && r.Method == http.MethodPost:
		if f.merge != nil {
			return f.merge(id, body)
		}
		pr["state"] = "MERGED"
		return http.StatusOK, pr
	case r.Method == http.MethodGet:
		return http.StatusOK, pr
	case r.Method == http.MethodPut:
		f.puts = append(f.puts, body)
		for k, v := range body {
			pr[k] = v
		}
		return http.StatusOK, pr
	}
	return notFound()
}

func str(pr map[string]any, keys ...string) string {
	var v any = pr
	for _, k := range keys {
		m, _ := v.(map[string]any)
		v = m[k]
	}
	s, _ := v.(string)
	return s
}

// list evaluates the BBQL conditions the provider sends; repeated state
// conditions are alternatives.
func (f *fakeBitbucket) list(r *http.Request) any {
	conds := condRe.FindAllStringSubmatch(r.URL.Query().Get("q"), -1)
	var ids []int
	for id := range f.prs {
		ids = append(ids, id)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ids)))
	values := []any{}
	for _, id := range ids {
		pr := f.prs[id]
		ok, stateOK, sawState := true, false, false
		for _, c := range conds {
			want := strings.ReplaceAll(c[3], `\"`, `"`)
			switch c[1] {
			case "state":
				sawState = true
				stateOK = stateOK || pr["state"] == want
			case "source.branch.name":
				ok = ok && str(pr, "source", "branch", "name") == want
			case "destination.branch.name":
				ok = ok && str(pr, "destination", "branch", "name") == want
			case "description":
				ok = ok && strings.Contains(strings.ToLower(str(pr, "description")), strings.ToLower(want))
			}
		}
		if ok && (!sawState || stateOK) {
			values = append(values, pr)
		}
	}
	// Two values per page, to exercise paging.
	pageNo, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageNo = max(pageNo, 1)
	lo, hi := min((pageNo-1)*2, len(values)), min(pageNo*2, len(values))
	out := map[string]any{"values": values[lo:hi]}
	if hi < len(values) {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(pageNo+1))
		out["next"] = f.srv.URL + r.URL.Path + "?" + q.Encode()
	}
	return out
}

func provider(t *testing.T, f *fakeBitbucket) protocol.Provider {
	t.Helper()
	env := map[string]string{"BITBUCKET_TOKEN": "tok"}
	p, err := Plugin(Options{
		BaseURL: f.srv.URL, Getenv: func(k string) string { return env[k] },
		GitCredential: func(string) (string, string, error) { return "", "", errors.New("none") },
		Sleep:         func(time.Duration) {},
	}).Open(protocol.Repo{Host: Host, Path: "ws/repo"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGetCRMapsFields(t *testing.T) {
	f := newFake(t)
	f.addPR("OPEN", "feat", "main", map[string]any{"draft": true, "description": "body", "close_source_branch": true})
	f.addPR("DECLINED", "old", "main", nil)
	f.addPR("OPEN", "forked", "main", map[string]any{"source": endpoint("forked", "abc", "someone/repo")})
	p := provider(t, f)
	cr, err := p.GetCR(1)
	if err != nil {
		t.Fatal(err)
	}
	want := protocol.CR{Number: 1, Title: "T1", Body: "body", State: protocol.StateOpen, Draft: true, Head: "feat",
		Base: "main", URL: "https://bitbucket.org/ws/repo/pull-requests/1", HeadSHA: "shafeat", DeleteHead: true}
	if *cr != want {
		t.Fatalf("GetCR =\n%+v\nwant\n%+v", *cr, want)
	}
	if cr, _ := p.GetCR(2); cr.State != protocol.StateClosed {
		t.Fatalf("declined PR state = %q", cr.State)
	}
	if cr, _ := p.GetCR(3); !cr.FromFork {
		t.Fatalf("PR from fork not recognised: %+v", cr)
	}
	if _, err := p.GetCR(9); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatalf("missing PR error = %v (%s)", err, protocol.CodeOf(err))
	}
	if f.auth[0] != "Bearer tok" {
		t.Fatalf("Authorization = %q", f.auth[0])
	}
}

func TestFindCRForBranchPrefersOpenAndSkipsForks(t *testing.T) {
	f := newFake(t)
	f.addPR("MERGED", "feat", "main", nil)
	f.addPR("OPEN", "feat", "main", map[string]any{"source": endpoint("feat", "x", "fork/repo")})
	f.addPR("DECLINED", "feat", "main", nil)
	p := provider(t, f)
	cr, err := p.FindCRForBranch("feat")
	if err != nil || cr == nil || cr.Number != 1 {
		t.Fatalf("FindCRForBranch = %+v, %v", cr, err)
	}
	f.addPR("OPEN", "feat", "main", nil)
	if cr, _ := p.FindCRForBranch("feat"); cr == nil || cr.Number != 4 {
		t.Fatalf("open PR not preferred: %+v", cr)
	}
	if cr, err := p.FindCRForBranch(`we"ird`); cr != nil || err != nil {
		t.Fatalf("FindCRForBranch(none) = %+v, %v", cr, err)
	}
}

func TestSearchAndListTargetingFollowPages(t *testing.T) {
	f := newFake(t)
	f.addPR("OPEN", "a", "main", map[string]any{"description": "x\n[//]: # (git-stack:begin {})"})
	f.addPR("OPEN", "b", "main", map[string]any{"description": "GIT-STACK:BEGIN in capitals"})
	f.addPR("OPEN", "c", "a", nil)
	f.addPR("OPEN", "d", "a", nil)
	f.addPR("OPEN", "e", "a", nil)
	f.addPR("OPEN", "f", "a", map[string]any{"source": endpoint("f", "x", "fork/repo")})
	f.addPR("MERGED", "g", "a", nil)
	p := provider(t, f)
	if got, err := p.SearchCRs("git-stack:begin"); err != nil || len(got) != 1 || got[0].Number != 1 {
		t.Fatalf("SearchCRs = %+v, %v", got, err)
	}
	got, err := p.ListCRsTargeting("a")
	if err != nil {
		t.Fatal(err)
	}
	var nums []int
	for _, cr := range got {
		nums = append(nums, cr.Number)
	}
	if fmt.Sprint(nums) != "[5 4 3]" {
		t.Fatalf("ListCRsTargeting = %v", nums)
	}
}

func TestCreateAndUpdateKeepReviewers(t *testing.T) {
	f := newFake(t)
	p := provider(t, f)
	cr, err := p.CreateCR(protocol.NewCR{Head: "feat", Base: "main", Title: "Feat", Body: "b", Draft: true})
	if err != nil {
		t.Fatal(err)
	}
	if cr.Number != 1 || !cr.Draft || cr.Head != "feat" || cr.Base != "main" || cr.Body != "b" {
		t.Fatalf("CreateCR = %+v", cr)
	}
	if f.prs[1]["close_source_branch"] != false {
		t.Fatalf("close_source_branch = %v", f.prs[1]["close_source_branch"])
	}
	f.prs[1]["reviewers"] = []any{map[string]any{"uuid": "{r1}", "display_name": "R"}}
	base, body := "dev", "new"
	if cr, err = p.UpdateCR(1, protocol.Update{Base: &base, Body: &body}); err != nil {
		t.Fatal(err)
	}
	if cr.Base != "dev" || cr.Body != "new" || cr.Title != "Feat" {
		t.Fatalf("UpdateCR = %+v", cr)
	}
	put, _ := json.Marshal(f.puts[0])
	if !strings.Contains(string(put), `"reviewers":[{"uuid":"{r1}"}]`) || !strings.Contains(string(put), `"title":"Feat"`) {
		t.Fatalf("PUT body = %s", put)
	}
	if cr, err = p.MarkReady(1); err != nil || cr.Draft {
		t.Fatalf("MarkReady = %+v, %v", cr, err)
	}
}

func TestMergeStrategiesAndStaleHead(t *testing.T) {
	f := newFake(t)
	id := f.addPR("OPEN", "feat", "main", nil)
	var got map[string]any
	f.merge = func(id int, body map[string]any) (int, any) {
		got = body
		f.prs[id]["state"] = "MERGED"
		return http.StatusOK, f.prs[id]
	}
	p := provider(t, f)
	if _, err := p.Merge(id, protocol.MethodRebase, "otherhash"); protocol.CodeOf(err) != protocol.CodeFailure || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("stale head error = %v", err)
	}
	cr, err := p.Merge(id, protocol.MethodRebase, "shafeat0123456789")
	if err != nil || !cr.Merged() {
		t.Fatalf("Merge = %+v, %v", cr, err)
	}
	if got["merge_strategy"] != "fast_forward" || got["close_source_branch"] != false {
		t.Fatalf("merge body = %v", got)
	}
	for method, want := range map[string]string{protocol.MethodMerge: "merge_commit", "": "merge_commit", protocol.MethodSquash: "squash"} {
		f.prs[id]["state"] = "OPEN"
		if _, err := p.Merge(id, method, ""); err != nil || got["merge_strategy"] != want {
			t.Fatalf("Merge(%q) strategy = %v, %v", method, got["merge_strategy"], err)
		}
	}
}

func TestMergeWaitsForAsyncTask(t *testing.T) {
	f := newFake(t)
	id := f.addPR("OPEN", "feat", "main", nil)
	f.merge = func(id int, _ map[string]any) (int, any) {
		f.prs[id]["state"] = "MERGED" // as it will be once the task succeeds
		return http.StatusAccepted, map[string]any{"task_status": "PENDING", "links": map[string]any{"self": map[string]any{"href": f.srv.URL + "/task-status/1"}}}
	}
	f.tasks["1"] = []any{
		map[string]any{"task_status": "PENDING"},
		map[string]any{"task_status": "SUCCESS"},
	}
	cr, err := provider(t, f).Merge(id, protocol.MethodMerge, "")
	if err != nil || !cr.Merged() {
		t.Fatalf("Merge = %+v, %v", cr, err)
	}
	polls := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, "GET /task-status/") {
			polls++
		}
	}
	if polls != 2 {
		t.Fatalf("task polls = %d, calls %v", polls, f.calls)
	}
}

func TestMergeRefusedIsFailure(t *testing.T) {
	f := newFake(t)
	id := f.addPR("OPEN", "feat", "main", nil)
	f.merge = func(int, map[string]any) (int, any) {
		return http.StatusBadRequest, map[string]any{"type": "error", "error": map[string]any{"message": "You can't merge until checks pass"}}
	}
	_, err := provider(t, f).Merge(id, protocol.MethodMerge, "")
	if protocol.CodeOf(err) != protocol.CodeFailure || !strings.Contains(err.Error(), "checks pass") {
		t.Fatalf("Merge error = %v (%s)", err, protocol.CodeOf(err))
	}
}

func TestGetBranch(t *testing.T) {
	f := newFake(t)
	f.restrictions = map[string]any{"values": []any{
		map[string]any{"kind": "push", "branch_match_kind": "glob", "pattern": "release/*"},
		map[string]any{"kind": "require_approvals_to_merge", "branch_match_kind": "glob", "pattern": "feat"},
		map[string]any{"kind": "delete", "branch_match_kind": "branching_model", "branch_type": "production"},
	}}
	p := provider(t, f)
	for name, want := range map[string]protocol.Branch{
		"main":          {Default: true},
		"release/1.0/x": {Protected: true},
		"feat":          {},
	} {
		b, err := p.GetBranch(name)
		if err != nil || *b != want {
			t.Errorf("GetBranch(%s) = %+v, %v; want %+v", name, b, err, want)
		}
	}
	f.restrictions = http.StatusForbidden
	if b, err := p.GetBranch("main"); err != nil || !b.Default || b.Protected {
		t.Fatalf("GetBranch without admin = %+v, %v", b, err)
	}
}

func TestGetRepoMapsStrategy(t *testing.T) {
	f := newFake(t)
	p := provider(t, f)
	for strategy, want := range map[string]string{
		"": "merge", "merge_commit": "merge", "squash": "squash", "squash_fast_forward": "squash",
		"fast_forward": "rebase", "rebase_merge": "rebase",
	} {
		f.strategy = strategy
		s, err := p.GetRepo()
		if err != nil || s.DefaultMergeMethod != want {
			t.Errorf("GetRepo with %q = %+v, %v; want %s", strategy, s, err, want)
		}
	}
}

func TestErrorsAreClassified(t *testing.T) {
	f := newFake(t)
	p := provider(t, f).(*Provider)
	for status, code := range map[int]string{
		http.StatusUnauthorized: protocol.CodeAuth, http.StatusForbidden: protocol.CodeAuth,
		http.StatusNotFound: protocol.CodeNotFound, http.StatusInternalServerError: protocol.CodeFailure,
	} {
		f.restrictions = status
		_, err := p.C.do(http.MethodGet, repoPath+"/branch-restrictions", nil, nil)
		if protocol.CodeOf(err) != code {
			t.Errorf("HTTP %d -> %v (%s), want %s", status, err, protocol.CodeOf(err), code)
		}
	}
}

func TestOpenRejectsNestedPath(t *testing.T) {
	_, err := Plugin(Options{Getenv: func(string) string { return "x" }}).Open(protocol.Repo{Host: Host, Path: "a/b/c"})
	if err == nil {
		t.Fatal("Open accepted a nested path")
	}
}

func TestCredentialsOrder(t *testing.T) {
	git := func(host string) (string, string, error) {
		if host != Host {
			return "", "", errors.New("wrong host")
		}
		return "gituser", "gitpass", nil
	}
	header := func(env map[string]string, git GitCredential) string {
		auth, err := Credentials(func(k string) string { return env[k] }, git)
		if err != nil {
			return "error: " + protocol.CodeOf(err)
		}
		req, _ := http.NewRequest(http.MethodGet, "https://x", nil)
		auth(req)
		return req.Header.Get("Authorization")
	}
	basic := func(u, p string) string {
		req, _ := http.NewRequest(http.MethodGet, "https://x", nil)
		req.SetBasicAuth(u, p)
		return req.Header.Get("Authorization")
	}
	all := map[string]string{"BITBUCKET_TOKEN": "t", "BITBUCKET_EMAIL": "e@x", "BITBUCKET_API_TOKEN": "a"}
	if got := header(all, git); got != "Bearer t" {
		t.Errorf("token: %q", got)
	}
	delete(all, "BITBUCKET_TOKEN")
	if got := header(all, git); got != basic("e@x", "a") {
		t.Errorf("api token: %q", got)
	}
	if got := header(map[string]string{"BITBUCKET_EMAIL": "e@x"}, git); got != basic("gituser", "gitpass") {
		t.Errorf("git credential: %q", got)
	}
	token := func(string) (string, string, error) { return "x-token-auth", "repotok", nil }
	if got := header(nil, token); got != "Bearer repotok" {
		t.Errorf("git access token: %q", got)
	}
	none := func(string) (string, string, error) { return "", "", errors.New("none") }
	if got := header(nil, none); got != "error: "+protocol.CodeAuth {
		t.Errorf("no credentials: %q", got)
	}
}
