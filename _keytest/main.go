package main
// _keytest/main.go — print raw byte values for each key press.
// Run with:  go run ./_keytest/
// Press q to quit.
package main

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

func main() {
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "MakeRaw:", err)
		os.Exit(1)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	fmt.Fprintf(os.Stderr, "Raw mode active. Press keys (q to quit).\r\n")
	buf := make([]byte, 8)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			break
		}
		fmt.Fprintf(os.Stderr, "bytes(%d):", n)
		for i := 0; i < n; i++ {
			fmt.Fprintf(os.Stderr, " 0x%02x", buf[i])
		}
		fmt.Fprintf(os.Stderr, "\r\n")
		if n == 1 && buf[0] == 'q' {
			break
		}
	}
}
