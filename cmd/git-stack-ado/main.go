// Command git-stack-ado is git-stack's Azure DevOps provider plugin. It uses
// the az CLI and its azure-devops extension, with az's login; the few calls az
// cannot make use $AZURE_DEVOPS_EXT_PAT or an az access token.
package main

import (
	"os"

	"github.com/mhvelplund/git-stack/internal/provider/ado"
	"github.com/mhvelplund/git-stack/pkg/protocol"
)

func main() {
	protocol.Serve(ado.Plugin(ado.ExecRunner, ado.NewREST(os.Getenv, ado.ExecRunner)))
}
