//go:build linux

package main

import (
	"bufio"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestFeedLensSendsThePointer(t *testing.T) {
	dir := t.TempDir()
	hypr, err := net.Listen("unix", filepath.Join(dir, "hypr.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer hypr.Close()
	go func() {
		for {
			conn, err := hypr.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 128)
			conn.Read(buf)
			io.WriteString(conn, hyprBatchReply)
			conn.Close()
		}
	}()

	feed, err := net.Listen("unix", filepath.Join(dir, "feed.sock"))
	if err != nil {
		t.Fatal(err)
	}
	quit := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		feedLens(feed, quit, filepath.Join(dir, "hypr.sock"))
		close(finished)
	}()

	lens, err := net.Dial("unix", filepath.Join(dir, "feed.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lens.Close()
	lens.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(lens).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if want := "5100 600 3840 0 2560 1440 0x55b9933fd940\n"; line != want {
		t.Fatalf("lens got %q, want %q", line, want)
	}
	close(quit)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("feedLens kept running after quit")
	}
}
