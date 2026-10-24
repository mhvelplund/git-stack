package ado

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/mhvelplund/git-stack/pkg/protocol"
)

// fakeADO answers the az commands the provider runs, and the REST calls, for
// repository org/proj/_git/repo.
type fakeADO struct {
	mu    sync.Mutex
	srv   *httptest.Server
	prs   map[int]map[string]any
	next  int
	calls [][]string // az arguments, or {"PATCH", id, body}
	auth  []string   // Authorization of REST calls
	// fail makes calls fail, by "az <first three args>" or "PATCH".
	fail          map[string]error
	policies      []any
	defaultBranch string
}

func newFake(t *testing.T) *fakeADO {
	f := &fakeADO{prs: map[int]map[string]any{}, next: 1, fail: map[string]error{}, defaultBranch: "refs/heads/main"}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serveREST))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeADO) addPR(status, head, base string, extra map[string]any) int {
	id := f.next
	f.next++
	pr := map[string]any{
		"pullRequestId": id, "title": "T" + strconv.Itoa(id), "description": "", "status": status, "isDraft": false,
		"sourceRefName": "refs/heads/" + head, "targetRefName": "refs/heads/" + base,
		"lastMergeSourceCommit": map[string]any{"commitId": "sha-" + head}, "mergeStatus": "succeeded",
		"repository": map[string]any{"name": "repo"},
	}
	for k, v := range extra {
		pr[k] = v
	}
	f.prs[id] = pr
	return id
}

// flags parses az arguments: flag values, and the key=value lists of the
// parameter flags. "@file" values are read from the file.
func flags(args []string) (map[string]string, map[string]string) {
	vals, pairs := map[string]string{}, map[string]string{}
	inList := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			if k, v, ok := strings.Cut(a, "="); ok && inList {
				pairs[k] = v
			}
			continue
		}
		inList = a == "--route-parameters" || a == "--query-parameters"
		if !inList && i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			i++
			v := args[i]
			if path, ok := strings.CutPrefix(v, "@"); ok {
				data, _ := os.ReadFile(path)
				v = string(data)
			}
			vals[a] = v
		}
	}
	return vals, pairs
}

func (f *fakeADO) Run(args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	if err := f.fail["az "+strings.Join(args[:min(3, len(args))], " ")]; err != nil {
		return nil, err
	}
	vals, pairs := flags(args)
	var out any
	switch strings.Join(args[:min(3, len(args))], " ") {
	case "account get-access-token --resource":
		return []byte("aad-token\n"), nil
	case "repos pr create":
		out = f.create(vals)
	case "repos pr update":
		var err error
		if out, err = f.update(vals); err != nil {
			return nil, err
		}
	case "devops invoke --area":
		var err error
		if out, err = f.invoke(vals, pairs); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unexpected az %v", args)
	}
	return json.Marshal(out)
}

func (f *fakeADO) create(v map[string]string) any {
	id := f.addPR("active", v["--source-branch"], v["--target-branch"], map[string]any{
		"title": v["--title"], "description": v["--description"], "isDraft": v["--draft"] == "true",
	})
	return f.prs[id]
}

func (f *fakeADO) update(v map[string]string) (any, error) {
	id, _ := strconv.Atoi(v["--id"])
	pr := f.prs[id]
	if pr == nil {
		return nil, errors.New("ERROR: TF401180: The requested pull request was not found.")
	}
	if t, ok := v["--title"]; ok {
		pr["title"] = t
	}
	if d, ok := v["--description"]; ok {
		pr["description"] = d
	}
	if d, ok := v["--draft"]; ok {
		pr["isDraft"] = d == "true"
	}
	if a, ok := v["--auto-complete"]; ok {
		id := noIdentity
		if a == "true" {
			id = "me"
		}
		pr["autoCompleteSetBy"] = map[string]any{"id": id}
	}
	opts, _ := pr["completionOptions"].(map[string]any)
	if opts == nil {
		opts = map[string]any{}
	}
	if s, ok := v["--squash"]; ok {
		opts["squashMerge"] = s == "true"
	}
	if d, ok := v["--delete-source-branch"]; ok {
		opts["deleteSourceBranch"] = d == "true"
	}
	pr["completionOptions"] = opts
	if v["--status"] == "completed" {
		pr["status"] = "completed"
	}
	return pr, nil
}

func (f *fakeADO) invoke(v, p map[string]string) (any, error) {
	switch v["--area"] + "/" + v["--resource"] {
	case "git/repositories":
		return map[string]any{"id": "repo-id", "defaultBranch": f.defaultBranch}, nil
	case "policy/configurations":
		return map[string]any{"count": len(f.policies), "value": f.policies}, nil
	}
	if id, ok := p["pullRequestId"]; ok {
		n, _ := strconv.Atoi(id)
		if pr := f.prs[n]; pr != nil {
			return pr, nil
		}
		return nil, errors.New("ERROR: TF401180: The requested pull request was not found.")
	}
	var ids []int
	for id := range f.prs {
		ids = append(ids, id)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ids)))
	var all []any
	for _, id := range ids {
		pr := f.prs[id]
		if s := p["searchCriteria.status"]; s != "" && s != "all" && pr["status"] != s {
			continue
		}
		if s := p["searchCriteria.sourceRefName"]; s != "" && pr["sourceRefName"] != s {
			continue
		}
		if s := p["searchCriteria.targetRefName"]; s != "" && pr["targetRefName"] != s {
			continue
		}
		listed := map[string]any{}
		for k, v := range pr {
			listed[k] = v
		}
		if d, _ := pr["description"].(string); len(d) > listedDescriptionMax {
			listed["description"] = d[:listedDescriptionMax]
		}
		all = append(all, listed)
	}
	skip, _ := strconv.Atoi(p["$skip"])
	top, _ := strconv.Atoi(p["$top"])
	lo, hi := min(skip, len(all)), min(skip+top, len(all))
	return map[string]any{"count": hi - lo, "value": all[lo:hi]}, nil
}

var restPath = regexp.MustCompile(`^/org/proj/_apis/git/repositories/repo/pullrequests/(\d+)$`)

func (f *fakeADO) serveREST(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	data, _ := json.Marshal(body)
	m := restPath.FindStringSubmatch(r.URL.Path)
	f.calls = append(f.calls, []string{r.Method, strings.Join(m[1:], ""), string(data)})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := f.fail["PATCH"]; err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": err.Error()})
		return
	}
	id, _ := strconv.Atoi(m[1])
	pr := f.prs[id]
	if r.Method != http.MethodPatch || r.URL.Query().Get("api-version") != apiVersion || pr == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "not found"})
		return
	}
	for k, v := range body {
		pr[k] = v
	}
	_ = json.NewEncoder(w).Encode(pr)
}

func (f *fakeADO) repo() protocol.Repo {
	return protocol.Repo{Host: strings.TrimPrefix(f.srv.URL, "https://"), Path: "org/proj/_git/repo"}
}

func provider(t *testing.T, f *fakeADO, env map[string]string) *Provider {
	t.Helper()
	rest := NewREST(func(k string) string { return env[k] }, f.Run)
	rest.HTTP = f.srv.Client()
	p, err := Plugin(f.Run, rest).Open(f.repo())
	if err != nil {
		t.Fatal(err)
	}
	return p.(*Provider)
}

func (f *fakeADO) last() []string { return f.calls[len(f.calls)-1] }

func TestParseRepo(t *testing.T) {
	for _, c := range []struct {
		host, path string
		want       Repository
	}{
		{"dev.azure.com", "org/My Proj/_git/repo", Repository{"https://dev.azure.com/org", "My Proj", "repo"}},
		{"org.visualstudio.com", "proj/_git/repo", Repository{"https://org.visualstudio.com", "proj", "repo"}},
		{"org.visualstudio.com", "DefaultCollection/proj/_git/repo", Repository{"https://org.visualstudio.com/DefaultCollection", "proj", "repo"}},
		{"server", "tfs/Coll/proj/_git/repo", Repository{"https://server/tfs/Coll", "proj", "repo"}},
	} {
		got, err := ParseRepo(protocol.Repo{Host: c.host, Path: c.path})
		if err != nil || got != c.want {
			t.Errorf("ParseRepo(%s, %s) = %+v, %v; want %+v", c.host, c.path, got, err, c.want)
		}
	}
	for _, bad := range []string{"owner/repo", "_git/repo", "org/proj/_git", "org/proj/_git/repo/extra"} {
		if _, err := ParseRepo(protocol.Repo{Host: "dev.azure.com", Path: bad}); err == nil {
			t.Errorf("ParseRepo(%q) should fail", bad)
		}
	}
	p := Plugin(nil, nil)
	if !p.Detect(protocol.Repo{Host: "dev.azure.com", Path: "o/p/_git/r"}) || p.Detect(protocol.Repo{Host: "git.example", Path: "a/b"}) {
		t.Error("Detect should match /_git/ paths only")
	}
}

func TestGetCRMapsFields(t *testing.T) {
	f := newFake(t)
	f.addPR("active", "feat", "main", map[string]any{
		"isDraft": true, "description": "body", "mergeStatus": "queued",
		"autoCompleteSetBy": map[string]any{"id": "u1"}, "completionOptions": map[string]any{"deleteSourceBranch": true},
	})
	f.addPR("completed", "old", "main", map[string]any{"completionQueueTime": "then"})
	f.addPR("abandoned", "gone", "main", nil)
	f.addPR("active", "merging", "main", map[string]any{"completionQueueTime": "now"})
	f.addPR("active", "forked", "main", map[string]any{"forkSource": map[string]any{"name": "refs/heads/forked"}})
	p := provider(t, f, nil)
	cr, err := p.GetCR(1)
	if err != nil {
		t.Fatal(err)
	}
	want := protocol.CR{Number: 1, Title: "T1", Body: "body", State: protocol.StateOpen, Draft: true, Head: "feat",
		Base: "main", URL: f.srv.URL + "/org/proj/_git/repo/pullrequest/1", HeadSHA: "sha-feat",
		AutoMerge: true, DeleteHead: true, Checking: true}
	if *cr != want {
		t.Fatalf("GetCR =\n%+v\nwant\n%+v", *cr, want)
	}
	if args := strings.Join(f.last(), " "); !strings.Contains(args, "--route-parameters project=proj pullRequestId=1") || !strings.Contains(args, "--organization "+f.srv.URL+"/org") {
		t.Fatalf("az %s", args)
	}
	for id, state := range map[int]string{2: protocol.StateMerged, 3: protocol.StateClosed, 4: protocol.StateMerging} {
		if cr, _ := p.GetCR(id); cr.State != state {
			t.Errorf("PR %d state = %q, want %q", id, cr.State, state)
		}
	}
	if cr, _ := p.GetCR(5); !cr.FromFork {
		t.Error("PR from fork not recognised")
	}
	if _, err := p.GetCR(99); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatalf("missing PR error = %v (%s)", err, protocol.CodeOf(err))
	}
}

func TestFindCRForBranchPrefersActiveAndSkipsForksAndOtherRepos(t *testing.T) {
	f := newFake(t)
	f.addPR("completed", "feat", "main", nil)
	f.addPR("completed", "feat", "main", nil)
	f.addPR("active", "feat", "main", map[string]any{"forkSource": map[string]any{"name": "x"}})
	f.addPR("active", "feat", "main", map[string]any{"repository": map[string]any{"name": "other"}})
	f.addPR("abandoned", "feat", "main", nil)
	p := provider(t, f, nil)
	if cr, err := p.FindCRForBranch("feat"); err != nil || cr == nil || cr.Number != 2 {
		t.Fatalf("FindCRForBranch = %+v, %v", cr, err)
	}
	if args := strings.Join(f.last(), " "); !strings.Contains(args, "searchCriteria.sourceRefName=refs/heads/feat") || !strings.Contains(args, "searchCriteria.status=all") {
		t.Fatalf("az %s", args)
	}
	f.addPR("active", "feat", "main", nil)
	if cr, _ := p.FindCRForBranch("feat"); cr == nil || cr.Number != 6 {
		t.Fatalf("active PR not preferred: %+v", cr)
	}
	if cr, err := p.FindCRForBranch("none"); cr != nil || err != nil {
		t.Fatalf("FindCRForBranch(none) = %+v, %v", cr, err)
	}
}

func TestSearchFetchesTruncatedDescriptions(t *testing.T) {
	f := newFake(t)
	long := strings.Repeat("x", 500) + "\n<!-- git-stack:begin {} -->"
	f.addPR("active", "a", "main", map[string]any{"description": "short <!-- git-stack:begin {} -->"})
	f.addPR("active", "b", "main", map[string]any{"description": long})
	f.addPR("active", "c", "main", map[string]any{"description": strings.Repeat("y", 500)})
	f.addPR("completed", "d", "main", map[string]any{"description": "git-stack:begin"})
	f.addPR("active", "e", "main", nil)
	p := provider(t, f, nil)
	got, err := p.SearchCRs("git-stack:begin")
	if err != nil {
		t.Fatal(err)
	}
	var nums []int
	for _, cr := range got {
		nums = append(nums, cr.Number)
	}
	if fmt.Sprint(nums) != "[2 1]" {
		t.Fatalf("SearchCRs = %v", nums)
	}
	gets := 0
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c, " "), "pullRequestId=") {
			gets++
		}
	}
	if gets != 2 { // PRs 2 and 3, whose listed descriptions were cut
		t.Fatalf("fetched %d PRs in full, want 2", gets)
	}
}

func TestListCRsTargetingPages(t *testing.T) {
	f := newFake(t)
	for i := 0; i < pageSize+5; i++ {
		f.addPR("active", fmt.Sprint("b", i), "a", nil)
	}
	f.addPR("active", "fork", "a", map[string]any{"forkSource": map[string]any{"name": "x"}})
	f.addPR("active", "other", "main", nil)
	got, err := provider(t, f, nil).ListCRsTargeting("a")
	if err != nil || len(got) != pageSize+5 {
		t.Fatalf("ListCRsTargeting = %d PRs, %v", len(got), err)
	}
	if args := strings.Join(f.last(), " "); !strings.Contains(args, fmt.Sprintf("$skip=%d", pageSize)) || !strings.Contains(args, "searchCriteria.targetRefName=refs/heads/a") {
		t.Fatalf("last az %s", args)
	}
}

func TestCreateAndUpdatePassTextThroughFiles(t *testing.T) {
	f := newFake(t)
	p := provider(t, f, nil)
	body := "line 1\n<!-- git-stack:begin {\"v\":1} -->\n& | % > <"
	cr, err := p.CreateCR(protocol.NewCR{Head: "feat", Base: "main", Title: "A & B", Body: body, Draft: true})
	if err != nil {
		t.Fatal(err)
	}
	if cr.Number != 1 || !cr.Draft || cr.Head != "feat" || cr.Base != "main" || cr.Body != body || cr.Title != "A & B" {
		t.Fatalf("CreateCR = %+v", cr)
	}
	for _, a := range f.last() {
		if strings.ContainsAny(a, "&\n") {
			t.Fatalf("title or body passed as an argument: %q", a)
		}
	}
	title, desc := "New & title", "new\nbody"
	if cr, err = p.UpdateCR(1, protocol.Update{Title: &title, Body: &desc}); err != nil || cr.Title != title || cr.Body != desc {
		t.Fatalf("UpdateCR = %+v, %v", cr, err)
	}
	if cr, err = p.MarkReady(1); err != nil || cr.Draft {
		t.Fatalf("MarkReady = %+v, %v", cr, err)
	}
	f.prs[1]["autoCompleteSetBy"] = map[string]any{"id": "u1"}
	if err := p.CancelAutoMerge(1); err != nil {
		t.Fatal(err)
	}
	if cr, _ = p.GetCR(1); cr.AutoMerge {
		t.Fatalf("auto-complete not cancelled: %+v", cr)
	}
	for _, c := range f.calls {
		if c[0] == http.MethodPatch {
			t.Fatalf("REST used for what az can do: %v", c)
		}
	}
}

func TestRetargetUsesREST(t *testing.T) {
	f := newFake(t)
	id := f.addPR("active", "b", "a", nil)
	base, title := "main", "T"
	cr, err := provider(t, f, map[string]string{patEnv: "pat"}).UpdateCR(id, protocol.Update{Base: &base, Title: &title})
	if err != nil || cr.Base != "main" || cr.Title != "T" {
		t.Fatalf("UpdateCR = %+v, %v", cr, err)
	}
	var patch []string
	for _, c := range f.calls {
		if c[0] == http.MethodPatch {
			patch = c
		}
	}
	if patch == nil || patch[2] != `{"targetRefName":"refs/heads/main"}` {
		t.Fatalf("PATCH = %v", patch)
	}
	if f.auth[0] != "Basic OnBhdA==" { // ":pat"
		t.Fatalf("Authorization = %q", f.auth[0])
	}
	// Without a PAT, az's access token is used.
	if _, err := provider(t, f, nil).UpdateCR(id, protocol.Update{Base: &base}); err != nil || f.auth[1] != "Bearer aad-token" {
		t.Fatalf("UpdateCR = %v, Authorization %q", err, f.auth[1])
	}
}

func TestRESTSignInPageIsAuthError(t *testing.T) {
	signIn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://login.example/", http.StatusFound)
	}))
	defer signIn.Close()
	rest := NewREST(func(string) string { return "pat" }, nil)
	rest.HTTP.Transport = signIn.Client().Transport
	repo := Repository{OrgURL: signIn.URL + "/org", Project: "p", Name: "r"}
	err := rest.PatchPR(repo, 1, map[string]any{}, nil)
	if protocol.CodeOf(err) != protocol.CodeAuth || !strings.Contains(err.Error(), patEnv) {
		t.Fatalf("PatchPR = %v (%s)", err, protocol.CodeOf(err))
	}
}

func TestMergeCompletes(t *testing.T) {
	for method, squash := range map[string]string{"": "false", protocol.MethodMerge: "false", protocol.MethodSquash: "true"} {
		f := newFake(t)
		id := f.addPR("active", "feat", "main", nil)
		cr, err := provider(t, f, nil).Merge(id, method, "sha-feat")
		if err != nil || !cr.Merged() {
			t.Fatalf("Merge(%q) = %+v, %v", method, cr, err)
		}
		v, _ := flags(f.last())
		if f.last()[2] != "update" || v["--status"] != "completed" || v["--squash"] != squash || v["--delete-source-branch"] != "false" {
			t.Fatalf("Merge(%q) ran az %v", method, f.last())
		}
	}
}

func TestMergeRefusesMovedHead(t *testing.T) {
	f := newFake(t)
	id := f.addPR("active", "feat", "main", nil)
	_, err := provider(t, f, nil).Merge(id, protocol.MethodMerge, "other")
	if protocol.CodeOf(err) != protocol.CodeFailure || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("Merge = %v", err)
	}
	if f.prs[id]["status"] != "active" {
		t.Fatal("moved PR was completed")
	}
}

func TestMergeRebaseUsesREST(t *testing.T) {
	f := newFake(t)
	id := f.addPR("active", "feat", "main", nil)
	cr, err := provider(t, f, map[string]string{patEnv: "pat"}).Merge(id, protocol.MethodRebase, "sha-feat")
	if err != nil || !cr.Merged() {
		t.Fatalf("Merge = %+v, %v", cr, err)
	}
	want := `{"completionOptions":{"deleteSourceBranch":false,"mergeStrategy":"rebase"},"lastMergeSourceCommit":{"commitId":"sha-feat"},"status":"completed"}`
	if c := f.last(); c[0] != http.MethodPatch || c[2] != want {
		t.Fatalf("last call = %v", c)
	}
}

func TestMergeFallsBackToAutoComplete(t *testing.T) {
	f := newFake(t)
	id := f.addPR("active", "feat", "main", nil)
	refused := errors.New("ERROR: TF401181: The pull request cannot be completed: policies not met")
	p := provider(t, f, nil)
	run := p.C.Run
	p.C.Run = func(args ...string) ([]byte, error) {
		if v, _ := flags(args); v["--status"] == "completed" {
			return nil, refused
		}
		return run(args...)
	}
	cr, err := p.Merge(id, protocol.MethodSquash, "sha-feat")
	if err != nil || !cr.AutoMerge || !cr.Open() {
		t.Fatalf("Merge = %+v, %v", cr, err)
	}
	if v, _ := flags(f.last()); v["--auto-complete"] != "true" || v["--squash"] != "true" {
		t.Fatalf("last az %v", f.last())
	}

	f.fail["az repos pr update"] = errors.New("ERROR: cannot set auto-complete")
	f.prs[id]["autoCompleteSetBy"] = nil
	cr, err = p.Merge(id, protocol.MethodMerge, "sha-feat")
	if err != nil || cr.AutoMerge || !strings.Contains(cr.MergeError, "cannot set auto-complete") {
		t.Fatalf("Merge with both failing = %+v, %v", cr, err)
	}
}

func TestGetBranchAndRepo(t *testing.T) {
	f := newFake(t)
	scope := func(repo, ref, kind string) map[string]any {
		return map[string]any{"repositoryId": repo, "refName": ref, "matchKind": kind}
	}
	policy := func(typ string, blocking bool, settings map[string]any) map[string]any {
		return map[string]any{"isEnabled": true, "isBlocking": blocking, "type": map[string]any{"id": typ}, "settings": settings}
	}
	f.policies = []any{
		policy("reviewers", true, map[string]any{"scope": []any{scope("repo-id", "refs/heads/release/", "Prefix")}}),
		policy("reviewers", false, map[string]any{"scope": []any{scope("", "refs/heads/dev", "Exact")}}),
		policy("reviewers", true, map[string]any{"scope": []any{scope("other-repo", "refs/heads/feat", "Exact")}}),
		policy(mergeStrategyPolicy, true, map[string]any{"scope": []any{scope("", "", "DefaultBranch")}, "allowSquash": true, "allowRebase": true}),
	}
	p := provider(t, f, nil)
	for name, want := range map[string]protocol.Branch{
		"main": {Default: true, Protected: true}, "release/1": {Protected: true}, "dev": {}, "feat": {},
	} {
		b, err := p.GetBranch(name)
		if err != nil || *b != want {
			t.Errorf("GetBranch(%s) = %+v, %v; want %+v", name, b, err, want)
		}
	}
	if s, err := p.GetRepo(); err != nil || s.DefaultMergeMethod != protocol.MethodSquash {
		t.Fatalf("GetRepo = %+v, %v", s, err)
	}
	f.policies = nil
	if s, err := p.GetRepo(); err != nil || s.DefaultMergeMethod != protocol.MethodMerge {
		t.Fatalf("GetRepo without policy = %+v, %v", s, err)
	}
}

func TestClassify(t *testing.T) {
	for msg, code := range map[string]string{
		"ERROR: TF401180: The requested pull request was not found.":     protocol.CodeNotFound,
		"ERROR: TF401019: The Git repository with name x does not exist": protocol.CodeNotFound,
		"ERROR: Please run 'az login' to setup account.":                 protocol.CodeAuth,
		"ERROR: TF400813: The user is not authorized":                    protocol.CodeAuth,
		"ERROR: something broke":                                         protocol.CodeFailure,
	} {
		if got := protocol.CodeOf(classify(errors.New(msg))); got != code {
			t.Errorf("classify(%q) = %s, want %s", msg, got, code)
		}
	}
}
