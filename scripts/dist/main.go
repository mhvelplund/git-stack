// Command dist cross-compiles every binary under ./cmd for each release
// platform into dist/<name>-<os>-<arch>[.exe]. Its only argument is passed to
// -ldflags.
package main

import (
	"fmt"
	"os"
	"os/exec"
)

var platforms = []struct{ os, arch string }{
	{"windows", "amd64"}, {"windows", "arm64"},
	{"linux", "amd64"}, {"linux", "arm64"},
	{"darwin", "amd64"}, {"darwin", "arm64"},
}

func main() {
	ldflags := ""
	if len(os.Args) > 1 {
		ldflags = os.Args[1]
	}
	cmds, err := os.ReadDir("cmd")
	if err != nil {
		fail(err)
	}
	for _, p := range platforms {
		ext := ""
		if p.os == "windows" {
			ext = ".exe"
		}
		for _, c := range cmds {
			if !c.IsDir() {
				continue
			}
			out := fmt.Sprintf("dist/%s-%s-%s%s", c.Name(), p.os, p.arch, ext)
			fmt.Println("building", out)
			cmd := exec.Command("go", "build", "-trimpath", "-ldflags="+ldflags, "-o", out, "./cmd/"+c.Name())
			cmd.Env = append(os.Environ(), "GOOS="+p.os, "GOARCH="+p.arch, "CGO_ENABLED=0")
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				fail(err)
			}
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
