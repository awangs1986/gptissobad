package openpage

import (
	"fmt"
	"os/exec"
	"runtime"
)

func Open(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("cannot open %s", url)
	}
	return cmd.Start()
}

func URL(port string) string {
	if port == "" {
		port = "18786"
	}
	return "http://127.0.0.1:" + port
}
