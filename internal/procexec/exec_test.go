//go:build darwin || linux

package procexec

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestMain(m *testing.M) {
	if len(os.Args) >= 3 && os.Args[1] == "--fd-helper" {
		switch os.Args[2] {
		case "check":
			fd, _ := strconv.Atoi(os.Args[3])
			_, err := fcntl(fd, syscall.F_GETFD, 0)
			if err == syscall.EBADF {
				fmt.Print("closed")
			} else if err == 0 {
				fmt.Print("open")
			} else {
				os.Exit(10)
			}
		case "explicit":
			input, _ := io.ReadAll(os.Stdin)
			extra, _ := io.ReadAll(os.NewFile(3, "explicit"))
			fmt.Printf("%s|%s", input, extra)
			fmt.Fprint(os.Stderr, "stderr")
		case "exec":
			if err := Exec(os.Args[0], []string{os.Args[0], "--fd-helper", "check", os.Args[3]}, os.Environ()); err != nil {
				fmt.Fprint(os.Stderr, err)
				os.Exit(11)
			}
		case "error":
			fmt.Fprint(os.Stdout, "out")
			fmt.Fprint(os.Stderr, strings.Repeat("e", 100000))
			os.Exit(7)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func leakFD(t *testing.T) int {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "non-cloexec-")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// F_DUPFD (not F_DUPFD_CLOEXEC) deliberately creates a leak. A high fd
	// prevents the child's own runtime from reusing the number after exec.
	fd, errno := fcntl(int(f.Fd()), syscall.F_DUPFD, 128)
	if errno != 0 {
		t.Fatal(errno)
	}
	t.Cleanup(func() { syscall.Close(int(fd)) })
	flags, errno := fcntl(int(fd), syscall.F_GETFD, 0)
	if errno != 0 || flags&syscall.FD_CLOEXEC != 0 {
		t.Fatal("fixture must be inheritable", flags, errno)
	}
	return int(fd)
}

func helper(mode string, fd int) *exec.Cmd {
	return exec.Command(os.Args[0], "--fd-helper", mode, strconv.Itoa(fd))
}

func TestNonCloexecFDDoesNotReachChild(t *testing.T) {
	for _, method := range []string{"Start", "Run", "Output", "CombinedOutput", "Exec"} {
		t.Run(method, func(t *testing.T) {
			fd := leakFD(t)
			// Deliberate unsafe control, the ONLY direct launch outside exec.go.
			control := helper("check", fd)
			if b, err := control.Output(); err != nil || string(b) != "open" {
				t.Fatalf("control: %q %v", b, err)
			}
			cmd := helper("check", fd)
			var b []byte
			var err error
			switch method {
			case "Start", "Run":
				var out bytes.Buffer
				cmd.Stdout = &out
				if method == "Start" {
					err = Start(cmd)
					if err == nil {
						err = cmd.Wait()
					}
				} else {
					err = Run(cmd)
				}
				b = out.Bytes()
			case "Output":
				b, err = Output(cmd)
			case "CombinedOutput":
				b, err = CombinedOutput(cmd)
			case "Exec":
				// Pass it deliberately to the helper with an ordinary fork/exec;
				// the helper's terminal Exec must sweep it before its own exec.
				control = helper("exec", fd)
				b, err = control.Output()
			}
			if err != nil || string(b) != "closed" {
				t.Fatalf("%s: %q %v", method, b, err)
			}
			flags, errno := fcntl(fd, syscall.F_GETFD, 0)
			if errno != 0 {
				t.Fatal("parent fd was closed", errno)
			}
			if method != "Exec" && flags&syscall.FD_CLOEXEC == 0 {
				t.Fatal("parent fd not marked CLOEXEC")
			}
			if _, errno := syscall.Write(fd, []byte("still usable")); errno != nil {
				t.Fatal(errno)
			}
		})
	}
}

func TestExplicitStdioAndExtraFilesSurvive(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "extra-")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.WriteString("extra")
	f.Seek(0, 0)
	stdin, err := os.CreateTemp(t.TempDir(), "stdin-")
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdin.WriteString("input")
	stdin.Seek(0, 0)
	stdout, err := os.CreateTemp(t.TempDir(), "stdout-")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	var stderr bytes.Buffer
	cmd := helper("explicit", 0)
	cmd.ExtraFiles, cmd.Stdin, cmd.Stdout, cmd.Stderr = []*os.File{f}, stdin, stdout, &stderr
	if err := Run(cmd); err != nil {
		t.Fatal(err)
	}
	stdout.Seek(0, 0)
	b, err := io.ReadAll(stdout)
	if err != nil || string(b) != "input|extra" || stderr.String() != "stderr" {
		t.Fatalf("%q %q %v", b, stderr.String(), err)
	}
}

func TestOutputPreservesExitAndBoundedStderr(t *testing.T) {
	actual, err := Output(helper("error", 0))
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 7 || string(actual) != "out" || len(ee.Stderr) > 66000 || !bytes.Contains(ee.Stderr, []byte("omitting 34464 bytes")) {
		t.Fatalf("output %q error %v", actual, err)
	}
	var w errorOutput
	for _, chunk := range []string{"a", strings.Repeat("b", 40000), strings.Repeat("c", 80000), "tail"} {
		w.Write([]byte(chunk))
	}
	if b := w.Bytes(); !bytes.HasPrefix(b, []byte("ab")) || !bytes.HasSuffix(b, []byte("tail")) || len(b) > 66000 {
		t.Fatal("bounded capture differs")
	}
}
