package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"
)

func helperArchive(t *testing.T, data []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := gzip.NewWriter(&buffer)
	if _, err := archive.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestUnpackHelper(t *testing.T) {
	data := []byte("MZtest embedded executable")
	archive := helperArchive(t, data)
	var output bytes.Buffer
	actual, err := unpackHelper(archive, &output, int64(len(data)))
	expected := sha256.Sum256(data)
	if err != nil || actual != hex.EncodeToString(expected[:]) || !bytes.Equal(output.Bytes(), data) {
		t.Fatalf("unpack: hash=%s data=%q err=%v", actual, output.Bytes(), err)
	}
	corrupt := append([]byte(nil), archive...)
	corrupt[len(corrupt)-8] ^= 0xff // Damage the gzip CRC without changing its payload.
	for _, test := range []struct {
		name    string
		payload []byte
		limit   int64
	}{
		{"truncated", archive[:len(archive)-1], 1024},
		{"corrupt checksum", corrupt, 1024},
		{"oversized", archive, int64(len(data) - 1)},
		{"empty executable", helperArchive(t, nil), 1024},
		{"extra gzip member", append(append([]byte(nil), archive...), archive...), 1024},
		{"trailing garbage", append(append([]byte(nil), archive...), 1), 1024},
	} {
		t.Run(test.name, func(t *testing.T) {
			hash, err := unpackHelper(test.payload, io.Discard, test.limit)
			if err == nil || hash != "" {
				t.Fatalf("unsafe archive accepted: hash=%s err=%v", hash, err)
			}
		})
	}
}

type failedHelperWriter struct{}

func (failedHelperWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestUnpackHelperWriteFailure(t *testing.T) {
	hash, err := unpackHelper(helperArchive(t, []byte("MZtest")), failedHelperWriter{}, 1024)
	if !errors.Is(err, io.ErrClosedPipe) || hash != "" {
		t.Fatalf("write failure ignored: hash=%s err=%v", hash, err)
	}
}
