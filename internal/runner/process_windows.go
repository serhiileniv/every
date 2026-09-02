//go:build windows

package runner

import (
	"os"
	"os/exec"
)

// Windows has no process groups in the POSIX sense, and passing the Setpgid
// attribute there is meaningless.
func setProcessGroup(c *exec.Cmd) {}

// terminate kills the process tree via taskkill.
//
// /T reaches the shell's descendants, which is the whole point -- killing just
// the cmd.exe wrapper would leave the actual command running. A Job Object
// would be the better primitive and can replace this without changing capture.
func terminate(p *os.Process) {
	if p == nil {
		return
	}
	cmd := exec.Command("taskkill.exe", "/PID", itoa(p.Pid), "/T", "/F")
	// Failure is not reported: the process may already be gone, and a missing
	// taskkill must not turn a timeout into a crash.
	_ = cmd.Run()
}

// Windows processes are not signalled, so there is never a 128+signum code.
func terminatingSignal(state *os.ProcessState) (int, bool) { return 0, false }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
