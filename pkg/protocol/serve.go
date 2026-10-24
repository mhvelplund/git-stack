package protocol

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Plugin is what a provider executable implements.
type Plugin struct {
	Info Info
	// Detect reports whether repo is hosted where this provider works, for
	// hosts git-stack does not know. It may be nil.
	Detect func(repo Repo) bool
	// Open returns a Provider for repo.
	Open func(repo Repo) (Provider, error)
}

// Serve runs one operation from the command line and exits. Call it from main.
func Serve(p Plugin) {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "%s%s is a git-stack provider plugin; run `git stack` instead.\n", ExecutablePrefix, p.Info.Name)
		os.Exit(2)
	}
	resp := Dispatch(p, os.Args[1], os.Stdin)
	_ = json.NewEncoder(os.Stdout).Encode(resp)
	if resp.Error != nil {
		os.Exit(1)
	}
}

// Dispatch decodes a request from r and runs op on p.
func Dispatch(p Plugin, op string, r io.Reader) Response {
	var req Request
	if op != OpDescribe {
		if err := json.NewDecoder(r).Decode(&req); err != nil {
			return errResponse(Errorf(CodeFailure, "decoding request: %v", err))
		}
		if req.Protocol != Version {
			return errResponse(Errorf(CodeUnsupported, "protocol version %d is not supported by %s (want %d)", req.Protocol, p.Info.Name, Version))
		}
	}
	resp, err := run(p, op, req)
	if err != nil {
		return errResponse(err)
	}
	return resp
}

func errResponse(err error) Response {
	return Response{Error: &Error{Code: CodeOf(err), Message: err.Error()}}
}

func run(p Plugin, op string, req Request) (Response, error) {
	switch op {
	case OpDescribe:
		info := p.Info
		info.Protocol = Version
		return Response{Info: &info}, nil
	case OpDetect:
		return Response{Match: p.Detect != nil && p.Detect(req.Repo)}, nil
	}
	prov, err := p.Open(req.Repo)
	if err != nil {
		return Response{}, err
	}
	switch op {
	case OpGetCR:
		cr, err := prov.GetCR(req.Number)
		return Response{CR: cr}, err
	case OpFindCRForBranch:
		cr, err := prov.FindCRForBranch(req.Branch)
		return Response{CR: cr}, err
	case OpSearchCRs:
		crs, err := prov.SearchCRs(req.Text)
		return Response{CRs: crs}, err
	case OpListCRsTargeting:
		crs, err := prov.ListCRsTargeting(req.Branch)
		return Response{CRs: crs}, err
	case OpCreateCR:
		if req.New == nil {
			return Response{}, Errorf(CodeFailure, "%s: missing new", op)
		}
		cr, err := prov.CreateCR(*req.New)
		return Response{CR: cr}, err
	case OpUpdateCR:
		if req.Update == nil {
			return Response{}, Errorf(CodeFailure, "%s: missing update", op)
		}
		cr, err := prov.UpdateCR(req.Number, *req.Update)
		return Response{CR: cr}, err
	case OpMarkReady:
		cr, err := prov.MarkReady(req.Number)
		return Response{CR: cr}, err
	case OpCancelAutoMerge:
		return Response{}, prov.CancelAutoMerge(req.Number)
	case OpMerge:
		cr, err := prov.Merge(req.Number, req.Method, req.SHA)
		return Response{CR: cr}, err
	case OpGetBranch:
		b, err := prov.GetBranch(req.Branch)
		return Response{Branch: b}, err
	case OpGetRepo:
		s, err := prov.GetRepo()
		return Response{Repo: s}, err
	case OpSyncStack:
		return Response{}, prov.SyncStack(req.Numbers)
	case OpUnstack:
		return Response{}, prov.Unstack(req.Numbers)
	}
	return Response{}, Errorf(CodeUnsupported, "%s does not support %q", p.Info.Name, op)
}
