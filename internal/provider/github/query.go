package github

import (
	"sort"
	"strings"
)

// prFields selects what prNode holds.
const prFields = `id number state url title body headRefName baseRefName headRefOid isDraft isCrossRepository mergeable mergeQueueEntry{id} autoMergeRequest{enabledAt}`

// prJSON is prNode as decoded from a hand-written query.
type prJSON struct {
	ID                string `json:"id"`
	Number            int    `json:"number"`
	State             string `json:"state"`
	URL               string `json:"url"`
	Title             string `json:"title"`
	Body              string `json:"body"`
	HeadRefName       string `json:"headRefName"`
	BaseRefName       string `json:"baseRefName"`
	HeadRefOid        string `json:"headRefOid"`
	IsDraft           bool   `json:"isDraft"`
	IsCrossRepository bool   `json:"isCrossRepository"`
	Mergeable         string `json:"mergeable"`
	MergeQueueEntry   *struct {
		ID string `json:"id"`
	} `json:"mergeQueueEntry"`
	AutoMergeRequest *struct {
		EnabledAt string `json:"enabledAt"`
	} `json:"autoMergeRequest"`
}

func (j *prJSON) node() *prNode {
	n := &prNode{
		ID: j.ID, Number: j.Number, State: j.State, URL: j.URL, Title: j.Title, Body: j.Body,
		HeadRefName: j.HeadRefName, BaseRefName: j.BaseRefName, HeadRefOid: j.HeadRefOid,
		IsDraft: j.IsDraft, IsCrossRepository: j.IsCrossRepository, Mergeable: j.Mergeable,
	}
	if j.MergeQueueEntry != nil {
		n.MergeQueueEntry = &MergeQueueEntry{ID: j.MergeQueueEntry.ID}
	}
	if j.AutoMergeRequest != nil {
		n.AutoMergeRequest = &AutoMergeRequest{EnabledAt: j.AutoMergeRequest.EnabledAt}
	}
	return n
}

// varDecls declares extra string variables for a query, e.g. ",$head:String!".
func varDecls(vars map[string]any) string {
	names := make([]string, 0, len(vars))
	for k := range vars {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		b.WriteString(",$" + k + ":String!")
	}
	return b.String()
}
