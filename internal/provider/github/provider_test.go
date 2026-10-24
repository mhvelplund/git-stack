package github

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"

	"gitlab.com/malephex/git-stack/pkg/protocol"
)

// fakeGitHub answers the REST and GraphQL calls the provider makes.
type fakeGitHub struct {
	mu    sync.Mutex
	prs   map[int]map[string]any // GraphQL pull request objects by number
	calls []string               // "METHOD path" or "graphql <first word of the operation>"
	// rest answers REST calls by "METHOD path"; missing ones are 404.
	rest map[string]func(body map[string]any) (int, any)
	gql  []map[string]any // variables of each GraphQL call
}

func newFake() *fakeGitHub {
	return &fakeGitHub{prs: map[int]map[string]any{}, rest: map[string]func(map[string]any) (int, any){}}
}

func (f *fakeGitHub) addPR(n int, state, head, base string, extra map[string]any) {
	pr := map[string]any{
		"id": "PR_" + head, "number": n, "state": state, "url": "https://github.com/o/r/pull/" + itoa(n),
		"title": "T" + itoa(n), "body": "", "headRefName": head, "baseRefName": base,
		"headRefOid": "sha-" + head, "isDraft": false, "isCrossRepository": false, "mergeable": "MERGEABLE",
		"mergeQueueEntry": nil, "autoMergeRequest": nil,
	}
	for k, v := range extra {
		pr[k] = v
	}
	f.prs[n] = pr
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func (f *fakeGitHub) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	if req.Body != nil {
		data, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(data, &body)
	}
	status, out := http.StatusNotFound, any(map[string]any{"message": "Not Found"})
	if req.URL.Path == "/graphql" {
		status, out = http.StatusOK, f.graphql(body)
	} else {
		key := req.Method + " " + strings.TrimPrefix(req.URL.Path, "/")
		if req.URL.RawQuery != "" {
			key += "?" + req.URL.RawQuery
		}
		f.calls = append(f.calls, key)
		if h := f.rest[key]; h != nil {
			status, out = h(body)
		}
	}
	data, _ := json.Marshal(out)
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status), Request: req,
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   io.NopCloser(bytes.NewReader(data)),
	}, nil
}

func (f *fakeGitHub) graphql(body map[string]any) any {
	query, _ := body["query"].(string)
	vars, _ := body["variables"].(map[string]any)
	f.gql = append(f.gql, vars)
	switch {
	case strings.Contains(query, "pullRequest(number:"):
		f.calls = append(f.calls, "graphql pr")
		n := int(vars["number"].(float64))
		pr := f.prs[n]
		var v any
		if pr != nil {
			v = pr
		}
		return map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": v}}}
	case strings.Contains(query, "pullRequests("):
		f.calls = append(f.calls, "graphql list")
		var nodes []any
		for n := 1; n <= 100; n++ {
			pr := f.prs[n]
			if pr == nil {
				continue
			}
			if h, ok := vars["head"]; ok && pr["headRefName"] != h {
				continue
			}
			if b, ok := vars["base"]; ok && pr["baseRefName"] != b {
				continue
			}
			nodes = append(nodes, pr)
		}
		return map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequests": map[string]any{"nodes": nodes}}}}
	case strings.Contains(query, "enablePullRequestAutoMerge"):
		f.calls = append(f.calls, "graphql automerge")
		in := vars["input"].(map[string]any)
		for _, pr := range f.prs {
			if pr["id"] == in["pullRequestId"] {
				pr["autoMergeRequest"] = map[string]any{"enabledAt": "now"}
			}
		}
		return map[string]any{"data": map[string]any{"enablePullRequestAutoMerge": map[string]any{"pullRequest": map[string]any{"id": in["pullRequestId"]}}}}
	}
	return map[string]any{"errors": []any{map[string]any{"message": "unexpected query: " + query}}}
}

func provider(t *testing.T, f *fakeGitHub) protocol.Provider {
	t.Helper()
	p, err := Plugin(api.ClientOptions{Transport: f, AuthToken: "test"}).Open(protocol.Repo{Host: "github.com", Path: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGetCRMapsFields(t *testing.T) {
	f := newFake()
	f.addPR(3, "OPEN", "feat", "main", map[string]any{"isDraft": true, "autoMergeRequest": map[string]any{"enabledAt": "x"}, "mergeable": "UNKNOWN"})
	f.addPR(4, "MERGED", "old", "main", nil)
	p := provider(t, f)
	cr, err := p.GetCR(3)
	if err != nil {
		t.Fatal(err)
	}
	want := &protocol.CR{Number: 3, Title: "T3", State: protocol.StateOpen, Draft: true, Head: "feat", Base: "main",
		URL: "https://github.com/o/r/pull/3", HeadSHA: "sha-feat", AutoMerge: true, Checking: true}
	if !reflect.DeepEqual(cr, want) {
		t.Fatalf("GetCR =\n%+v\nwant\n%+v", cr, want)
	}
	if cr, _ := p.GetCR(4); !cr.Merged() {
		t.Fatalf("#4 should be merged: %+v", cr)
	}
	if _, err := p.GetCR(9); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatalf("missing PR error = %v (%s)", err, protocol.CodeOf(err))
	}
}

func TestFindCRForBranchPrefersOpenAndSkipsForks(t *testing.T) {
	f := newFake()
	f.addPR(1, "MERGED", "feat", "main", nil)
	f.addPR(2, "OPEN", "feat", "main", map[string]any{"isCrossRepository": true})
	p := provider(t, f)
	cr, err := p.FindCRForBranch("feat")
	if err != nil || cr == nil || cr.Number != 1 {
		t.Fatalf("FindCRForBranch = %+v, %v", cr, err)
	}
	f.addPR(3, "OPEN", "feat", "main", nil)
	if cr, _ := p.FindCRForBranch("feat"); cr == nil || cr.Number != 3 {
		t.Fatalf("open PR not preferred: %+v", cr)
	}
	if cr, err := p.FindCRForBranch("none"); cr != nil || err != nil {
		t.Fatalf("FindCRForBranch(none) = %+v, %v", cr, err)
	}
}

func TestSearchAndListTargeting(t *testing.T) {
	f := newFake()
	f.addPR(1, "OPEN", "a", "main", map[string]any{"body": "x <!-- git-stack:begin {} -->"})
	f.addPR(2, "OPEN", "b", "a", nil)
	f.addPR(3, "OPEN", "c", "a", map[string]any{"isCrossRepository": true})
	p := provider(t, f)
	if got, err := p.SearchCRs("git-stack:begin"); err != nil || len(got) != 1 || got[0].Number != 1 {
		t.Fatalf("SearchCRs = %+v, %v", got, err)
	}
	if got, err := p.ListCRsTargeting("a"); err != nil || len(got) != 1 || got[0].Number != 2 {
		t.Fatalf("ListCRsTargeting = %+v, %v", got, err)
	}
}

func TestMergeFallsBackToAutoMerge(t *testing.T) {
	f := newFake()
	f.addPR(5, "OPEN", "feat", "main", nil)
	var merged map[string]any
	f.rest["PUT repos/o/r/pulls/5/merge"] = func(body map[string]any) (int, any) {
		merged = body
		return http.StatusMethodNotAllowed, map[string]any{"message": "Required status check is expected."}
	}
	p := provider(t, f)
	cr, err := p.Merge(5, protocol.MethodSquash, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if merged["merge_method"] != "squash" || merged["sha"] != "abc" {
		t.Fatalf("merge body = %v", merged)
	}
	if !cr.AutoMerge || !cr.Open() {
		t.Fatalf("PR should be open with auto-merge: %+v", cr)
	}
	in := f.gql[len(f.gql)-2]["input"].(map[string]any)
	if in["mergeMethod"] != "SQUASH" || in["expectedHeadOid"] != "abc" {
		t.Fatalf("auto-merge input = %v", in)
	}

	f.rest["PUT repos/o/r/pulls/5/merge"] = func(map[string]any) (int, any) {
		f.prs[5]["state"] = "MERGED"
		return http.StatusOK, map[string]any{"merged": true}
	}
	if cr, err := p.Merge(5, protocol.MethodMerge, "abc"); err != nil || !cr.Merged() {
		t.Fatalf("direct merge = %+v, %v", cr, err)
	}
}

func stack(number int, prs ...int) map[string]any {
	var list []any
	for _, n := range prs {
		list = append(list, map[string]any{"number": n, "state": "open", "head": map[string]any{"ref": "b", "sha": "s"}})
	}
	return map[string]any{"id": number, "number": number, "pull_requests": list}
}

func TestSyncStack(t *testing.T) {
	f := newFake()
	p := provider(t, f)

	// No stack yet: one is created from the PRs, bottom first.
	var created []any
	f.rest["GET repos/o/r/stacks?pull_request=1"] = func(map[string]any) (int, any) { return http.StatusOK, []any{} }
	f.rest["POST repos/o/r/stacks"] = func(b map[string]any) (int, any) {
		created = b["pull_requests"].([]any)
		return http.StatusCreated, stack(7, 1, 2)
	}
	if err := p.SyncStack([]int{1, 2}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(created, []any{1.0, 2.0}) {
		t.Fatalf("created stack with %v", created)
	}

	// An existing stack that is a prefix is extended with the new PRs only.
	var added []any
	f.rest["GET repos/o/r/stacks?pull_request=1"] = func(map[string]any) (int, any) { return http.StatusOK, []any{stack(7, 1, 2)} }
	f.rest["POST repos/o/r/stacks/7/add"] = func(b map[string]any) (int, any) {
		added = b["pull_requests"].([]any)
		return http.StatusOK, stack(7, 1, 2, 3)
	}
	if err := p.SyncStack([]int{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(added, []any{3.0}) {
		t.Fatalf("added %v", added)
	}

	// In sync: nothing is written.
	n := len(f.calls)
	if err := p.SyncStack([]int{1, 2}); err != nil || len(f.calls) != n+1 {
		t.Fatalf("in-sync stack made calls %v (%v)", f.calls[n:], err)
	}
}

func TestStacksAPIMissingFallsBackToBlocks(t *testing.T) {
	f := newFake()
	p := provider(t, f) // every stacks endpoint is 404
	if err := p.SyncStack([]int{1, 2}); err != nil {
		t.Fatalf("SyncStack without the Stacks API: %v", err)
	}
	n := len(f.calls)
	if err := p.SyncStack([]int{1, 2, 3}); err != nil || len(f.calls) != n {
		t.Fatalf("the Stacks API was asked again: %v (%v)", f.calls[n:], err)
	}
	if err := p.Unstack([]int{1}); err != nil {
		t.Fatal(err)
	}
}

func TestClassify(t *testing.T) {
	for status, want := range map[int]string{404: protocol.CodeNotFound, 401: protocol.CodeAuth, 403: protocol.CodeAuth, 500: protocol.CodeFailure} {
		err := classify(&api.HTTPError{StatusCode: status, Message: "x"})
		if protocol.CodeOf(err) != want {
			t.Errorf("classify(%d) = %s, want %s", status, protocol.CodeOf(err), want)
		}
	}
}
