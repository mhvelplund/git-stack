// Command git-stack-gitlab is git-stack's GitLab provider plugin.
package main

import (
	"github.com/mhvelplund/git-stack/internal/provider/gitlab"
	"github.com/mhvelplund/git-stack/pkg/protocol"
)

func main() {
	protocol.Serve(gitlab.Plugin(gitlab.ExecRunner))
}
