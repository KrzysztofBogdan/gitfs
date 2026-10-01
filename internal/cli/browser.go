package cli

import (
	"os/exec"
	"runtime"
)

// openBrowser opens u in the user's browser, best effort.
func openBrowser(u string) {
	name, args := "xdg-open", []string{u}
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", u}
	}
	if cmd := exec.Command(name, args...); cmd.Start() == nil {
		go cmd.Wait()
	}
}
