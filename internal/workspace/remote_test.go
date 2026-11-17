package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteSiteRejectsUnsafeTargetsBeforeListening(t *testing.T) {
	s, _ := fixture(t)
	for _, args := range [][4]string{
		{"example.com", "80", "/tmp/dopamine-site-01234567890123456789012345678901/s", "host"},
		{"127.0.0.1", "0", "/tmp/dopamine-site-01234567890123456789012345678901/s", "host"},
		{"127.0.0.1", "80", "/tmp/foreign.sock", "host"},
		{"127.0.0.1", "80", "/tmp/dopamine-site-01234567890123456789012345678901/s", "invalid"},
	} {
		if err := s.RemoteSite(args[0], args[1], args[2], args[3]); err == nil {
			t.Fatal("unsafe target accepted", args)
		}
	}
}

func TestRemotePasteProtectsShellsAndStaleCodexPanes(t *testing.T) {
	for _, panes := range []string{"%1|shell|token|0|0", "%1|codex|token|1|0", "%1|codex|token|0|1", "%1|codex||0|0"} {
		t.Run(panes, func(t *testing.T) {
			s, _ := fixture(t)
			useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "tmux" || args[0] != "list-panes" {
					t.Fatal("modified clipboard or shell", name, args)
				}
				return []byte(panes), nil
			}})
			if err := s.PasteRemoteClipboard("text/plain", strings.NewReader("hello\n")); err == nil {
				t.Fatal("paste accepted outside a live Codex pane")
			}
		})
	}
}

func TestRemotePastePreservesImageAndDoesNotSubmitText(t *testing.T) {
	for _, image := range []bool{false, true} {
		for _, stale := range []bool{false, true} {
			s, _ := fixture(t)
			mime, data := "text/plain", []byte("текст\nwith another line\n")
			if image {
				mime, data = "image/png", []byte("\x89PNG\r\n\x1a\nfixture")
			}
			var loaded []byte
			useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "tmux" {
					t.Fatal(name)
				}
				switch args[0] {
				case "list-panes":
					return []byte("%1|codex|0123456789abcdef|0|0"), nil
				case "load-buffer":
					var err error
					loaded, err = os.ReadFile(args[len(args)-1])
					return nil, err
				case "if-shell":
					if !strings.Contains(args[4], "0123456789abcdef") || !strings.Contains(args[5], "'paste-buffer' '-p' '-d'") || strings.Contains(args[5], "send-keys") {
						t.Fatal("paste lost identity, bracketed framing, or submits input", args)
					}
					if stale {
						return []byte("dopamine-paste-stale"), nil
					}
					return []byte("dopamine-paste-ok"), nil
				case "delete-buffer":
					return nil, nil
				default:
					t.Fatal(args)
				}
				return nil, nil
			}})
			err := s.PasteRemoteClipboard(mime, bytes.NewReader(data))
			if (err != nil) != stale {
				t.Fatal("wrong paste result", image, stale, err)
			}
			files, _ := filepath.Glob(filepath.Join(s.StateDir, "remote-uploads", "*"))
			if image && !stale {
				if len(files) != 1 || string(loaded) != files[0] {
					t.Fatal("image not pasted by its home path", files, string(loaded))
				}
				got, _ := os.ReadFile(files[0])
				info, _ := os.Stat(files[0])
				if !bytes.Equal(got, data) || info.Mode().Perm() != 0600 {
					t.Fatal("image changed or permissions are not private")
				}
			} else if len(files) != 0 {
				t.Fatal("temporary text or failed image upload was not cleaned", files)
			}
		}
	}
}

func TestRemotePasteRejectsInvalidClipboardBeforeCreatingFiles(t *testing.T) {
	s, _ := fixture(t)
	useRunner(t, stubRunner{output: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" || args[0] != "list-panes" {
			t.Fatal("invalid clipboard caused a paste", name, args)
		}
		return []byte("%1|codex|0123456789abcdef|0|0"), nil
	}})
	for _, test := range []struct{ mime, data string }{
		{"text/plain", ""}, {"text/plain", "escape\x1b[201~\n"}, {"text/plain", "\xff"},
		{"image/png", "not an image"}, {"image/svg+xml", "<svg/>"},
	} {
		if err := s.PasteRemoteClipboard(test.mime, strings.NewReader(test.data)); err == nil {
			t.Fatalf("invalid clipboard accepted: %q", test.mime)
		}
	}
	if _, err := os.Stat(filepath.Join(s.StateDir, "remote-uploads")); !os.IsNotExist(err) {
		t.Fatal("invalid clipboard created upload storage", err)
	}
}
