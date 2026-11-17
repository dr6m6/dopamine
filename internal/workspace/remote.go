package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

var remoteSocket = regexp.MustCompile(`^/tmp/dopamine-site-[0-9a-f]{32}/s$`)

// RemoteSite bridges one local target through a private filesystem socket.
// Unix sockets cross the existing VPN network namespace without new IP access.
func (s Service) RemoteSite(host, port, socket, network string) error {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port || !remoteSocket.MatchString(socket) {
		return errors.New("invalid remote site target or socket")
	}
	if host != "localhost" && host != "127.0.0.1" && host != "::1" && !(host == "10.203.31.1" && port == "8420") {
		return errors.New("remote site must use loopback or the existing CUTTER relay")
	}
	if network != "auto" && network != "host" && network != "vpn" && network != "inside-vpn" {
		return errors.New("invalid site network")
	}
	target := net.JoinHostPort(host, port)
	var probeErr error
	if network != "vpn" {
		var probe net.Conn
		probe, probeErr = net.DialTimeout("tcp", target, 3*time.Second)
		if probeErr == nil {
			probe.Close()
		}
	}
	if network == "vpn" || (network == "auto" && probeErr != nil) {
		launcher := "/run/current-system/sw/bin/vpn-run"
		if _, err := os.Stat(launcher); err != nil {
			return errors.New("site is unavailable on the home host and vpn-run is not installed; start the site first")
		}
		cmd := exec.Command(launcher, s.Executable, "remote-site", host, port, socket, "inside-vpn")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	if probeErr != nil {
		return fmt.Errorf("site is unavailable in %s network: %w", network, probeErr)
	}
	dir := filepath.Dir(socket)
	if err := os.Mkdir(dir, 0700); err != nil {
		return err // Never replace a pre-existing directory or symlink.
	}
	defer os.Remove(dir)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer os.Remove(socket)
	defer listener.Close()
	if err := os.Chmod(socket, 0600); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		cancel() // The SSH client's input pipe owns this relay's lifetime.
	}()
	go func() { <-ctx.Done(); listener.Close() }()
	if network == "inside-vpn" {
		network = "vpn"
	} else {
		network = "host"
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"ready": true, "network": network}); err != nil {
		return err
	}
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			defer connection.Close()
			upstream, err := net.DialTimeout("tcp", target, 5*time.Second)
			if err != nil {
				return
			}
			defer upstream.Close()
			done := make(chan struct{})
			go func() {
				_, _ = io.Copy(upstream, connection)
				_ = upstream.(*net.TCPConn).CloseWrite()
				close(done)
			}()
			_, _ = io.Copy(connection, upstream)
			_ = connection.(*net.UnixConn).CloseWrite()
			<-done
		}()
	}
}

const remoteClipboardLimit = 32 << 20

// PasteRemoteClipboard inserts only into one live Codex pane in the current
// tab. It never submits Enter, executes text in a shell, or changes a clipboard.
func (s Service) PasteRemoteClipboard(mime string, input io.Reader) error {
	extensions := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif", "text/plain": ".txt"}
	ext, ok := extensions[mime]
	if !ok {
		return errors.New("unsupported clipboard type")
	}
	panes, err := tmux("list-panes", "-t", "=dopamine", "-F", "#{pane_id}|#{@dopamine_kind}|#{@dopamine_token}|#{pane_dead}|#{pane_in_mode}")
	if err != nil {
		return err
	}
	var target []string
	for _, line := range strings.Split(panes, "\n") {
		fields := strings.Split(line, "|")
		if len(fields) == 5 && fields[1] == "codex" && fields[2] != "" && fields[3] == "0" && fields[4] == "0" {
			if target != nil {
				return errors.New("current tab has multiple Codex panes; select a tab with one Codex pane")
			}
			target = fields
		}
	}
	if target == nil {
		return errors.New("select a running Codex tab and leave tmux copy mode before pasting")
	}
	data, err := io.ReadAll(io.LimitReader(input, remoteClipboardLimit+1))
	if err != nil || len(data) == 0 || len(data) > remoteClipboardLimit {
		return errors.New("clipboard must contain 1 byte to 32 MiB")
	}
	if mime == "text/plain" {
		if !utf8.Valid(data) || strings.ContainsAny(string(data), "\x00\x1b") {
			return errors.New("clipboard text must be UTF-8 without terminal escape sequences")
		}
	} else if detected := imageMIME(data); detected != mime {
		return errors.New("clipboard image does not match its declared format")
	}
	dir := filepath.Join(s.StateDir, "remote-uploads")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, "clipboard-*"+ext)
	if err != nil {
		return err
	}
	name := file.Name()
	keep := false
	defer func() {
		file.Close()
		if !keep {
			os.Remove(name)
		}
	}()
	pasted := data
	if mime != "text/plain" {
		if _, err := file.Write(data); err != nil {
			return err
		}
		pasted = []byte(name)
	}
	if mime == "text/plain" {
		if _, err := file.Write(pasted); err != nil {
			return err
		}
	}
	if err := file.Close(); err != nil {
		return err
	}
	buffer := "dopamine-" + filepath.Base(name)
	if mime != "text/plain" {
		// Store the path separately; the image itself stays available to Codex.
		pathFile, err := os.CreateTemp(dir, "path-*.txt")
		if err != nil {
			return err
		}
		defer os.Remove(pathFile.Name())
		_, writeErr := pathFile.Write(pasted)
		closeErr := pathFile.Close()
		if writeErr != nil || closeErr != nil {
			return errors.New("could not prepare image attachment")
		}
		_, err = tmux("load-buffer", "-b", buffer, pathFile.Name())
	} else {
		_, err = tmux("load-buffer", "-b", buffer, name)
	}
	if err != nil {
		return err
	}
	defer tmux("delete-buffer", "-b", buffer)
	// Revalidate the exact pane and token inside tmux's command queue, so a
	// replacement process cannot receive the paste while the upload is running.
	condition := "#{&&:#{==:#{@dopamine_token}," + target[2] + "},#{&&:#{==:#{@dopamine_kind},codex},#{&&:#{==:#{pane_dead},0},#{==:#{pane_in_mode},0}}}}"
	result, err := tmux("if-shell", "-F", "-t", target[0], condition,
		ShellArgs([]string{"paste-buffer", "-p", "-d", "-r", "-b", buffer, "-t", target[0]})+" ; display-message -p dopamine-paste-ok",
		"display-message -p dopamine-paste-stale")
	if err != nil {
		return err
	}
	if result != "dopamine-paste-ok" {
		return errors.New("Codex pane changed during upload; nothing was pasted")
	}
	keep = mime != "text/plain"
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"pane": target[0], "type": mime})
}

func imageMIME(data []byte) string {
	if len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png"
	}
	if len(data) >= 3 && string(data[:3]) == "\xff\xd8\xff" {
		return "image/jpeg"
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return "image/gif"
	}
	return ""
}
