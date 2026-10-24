// Command git-stack-gitlab is git-stack's GitLab provider plugin.
package main

import (
	"gitlab.com/malephex/git-stack/internal/provider/gitlab"
	"gitlab.com/malephex/git-stack/pkg/protocol"
)

func main() {
	protocol.Serve(gitlab.Plugin(gitlab.ExecRunner))
}
