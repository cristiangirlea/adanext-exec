//go:build linux || darwin

package broker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAnAuditDirectoryAndFileMadeOpenAreMadeOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, time.Now().UTC().Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(name, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { // past the umask
		t.Fatal(err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		t.Fatal(err)
	}
	c := helper("exit")
	c.Audit = &Audit{Dir: dir}
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	di, _ := os.Stat(dir)
	fi, _ := os.Stat(name)
	if di.Mode().Perm() != 0o700 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("directory %v, file %v; want 0700 and 0600", di.Mode(), fi.Mode())
	}
}

func TestAnAuditFileThatIsNotARegularFileIsRefused(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02") + ".jsonl"
	for name, create := range map[string]func(string) error{
		"a link to /dev/null": func(p string) error { return os.Symlink("/dev/null", p) },
		"a pipe":              func(p string) error { return syscall.Mkfifo(p, 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := create(filepath.Join(dir, today)); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "ran")
			c := helper("touch", "ADANEXT_TEST_MARKER="+marker)
			c.Audit = &Audit{Dir: dir}
			done := make(chan error, 1)
			go func() { _, err := Run(context.Background(), c); done <- err }()
			select {
			case err := <-done:
				if !errors.Is(err, ErrAudit) {
					t.Fatalf("err %v, want ErrAudit", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("opening the audit blocked")
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("the command ran")
			}
		})
	}
}

func TestTheAuditRecordsASignalFromElsewhere(t *testing.T) {
	dir := t.TempDir()
	c := helper("selfterm")
	c.Timeout = time.Minute
	c.Audit = &Audit{Dir: dir}
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if l := readAudit(t, dir)[0]; l["outcome"] != "signalled" || l["signal"] != "terminated" || l["exit_code"] != float64(-1) {
		t.Fatalf("line %v, want signalled by SIGTERM with -1", l)
	}
}

func TestAnAuditOfAnotherUserIsRefused(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("making files of another user needs root")
	}
	const other = 12345
	for name, prepare := range map[string]func(dir, file string) error{
		"directory": func(dir, _ string) error { return os.Chown(dir, other, other) },
		"file": func(_, file string) error {
			if err := os.WriteFile(file, nil, 0o600); err != nil {
				return err
			}
			return os.Chown(file, other, other)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "audit")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := prepare(dir, filepath.Join(dir, time.Now().UTC().Format("2006-01-02")+".jsonl")); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "ran")
			c := helper("touch", "ADANEXT_TEST_MARKER="+marker)
			c.Audit = &Audit{Dir: dir}
			if _, err := Run(context.Background(), c); !errors.Is(err, ErrAudit) || !strings.Contains(err.Error(), "another user") {
				t.Fatalf("err %v, want ErrAudit naming another user", err)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("the command ran")
			}
		})
	}
}
