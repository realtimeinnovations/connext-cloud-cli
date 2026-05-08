package terminal

import (
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func StartProcess(cmd *exec.Cmd) (io.ReadCloser, io.ReadCloser, error) {
	if SupportsPTY() {
		master, slave, err := pty.Open()
		if err != nil {
			return nil, nil, err
		}
		cmd.Stdout = slave
		cmd.Stderr = slave
		if err := cmd.Start(); err != nil {
			_ = slave.Close()
			_ = master.Close()
			return nil, nil, err
		}
		_ = slave.Close()
		return master, nil, nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return stdout, stderr, nil
}

func SupportsPTY() bool {
	return runtime.GOOS != "windows"
}

func PlainOutputRequested(out io.Writer) bool {
	if envOptOut(os.Getenv("NO_COLOR")) || envOptOut(os.Getenv("CI")) || os.Getenv("TERM") == "dumb" {
		return true
	}
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	return !term.IsTerminal(int(file.Fd()))
}

func PromptFiles(in io.Reader, out io.Writer) (*os.File, *os.File, bool) {
	inputFile, ok := in.(*os.File)
	if !ok {
		return nil, nil, false
	}
	outputFile, ok := out.(*os.File)
	if !ok {
		return nil, nil, false
	}
	if !IsCharDevice(inputFile) || !IsCharDevice(outputFile) {
		return nil, nil, false
	}
	return inputFile, outputFile, true
}

func IsCharDevice(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func envOptOut(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no":
		return false
	default:
		return true
	}
}
