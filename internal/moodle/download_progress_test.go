package moodle

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadFileReportsProgress(t *testing.T) {
	size := 3*progressChunk + 123
	mux := http.NewServeMux()
	mux.HandleFunc("/big.bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(size))
		w.Write(make([]byte, size))
	})
	c, _ := testClient(t, mux)

	var reports []int64
	dst := filepath.Join(t.TempDir(), "big.bin")
	n, err := c.DownloadFile(context.Background(), c.BaseURL+"/big.bin", dst, func(done int64) {
		reports = append(reports, done)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(size) {
		t.Fatalf("bytes written = %d, want %d", n, size)
	}
	if len(reports) < 3 {
		t.Fatalf("got %d progress reports for %d bytes, want several", len(reports), size)
	}
	for i := 1; i < len(reports); i++ {
		if reports[i] < reports[i-1] {
			t.Fatalf("progress went backwards: %v", reports)
		}
	}
	if last := reports[len(reports)-1]; last != int64(size) {
		t.Fatalf("last progress report = %d, want %d", last, size)
	}
}

func TestDownloadFileWithoutProgressCallbackStillWorks(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/small.txt", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "tiny") })
	c, _ := testClient(t, mux)
	dst := filepath.Join(t.TempDir(), "small.txt")
	n, err := c.DownloadFile(context.Background(), c.BaseURL+"/small.txt", dst)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("n = %d, want 4", n)
	}
	if b, _ := os.ReadFile(dst); string(b) != "tiny" {
		t.Fatalf("content = %q", b)
	}
}
