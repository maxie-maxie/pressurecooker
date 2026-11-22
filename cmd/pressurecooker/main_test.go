package main

import (
	"io"
	"os"
	"testing"

	"github.com/maxie-maxie/pressurecooker/internal/cli"
)

func TestVersionOutput(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	original := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = original }()
	code := cli.Main([]string{"--version"}, version)
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(read)
	if err != nil || code != 0 || string(output) != "pressurecooker "+version+"\n" {
		t.Fatalf("version: %d %q %v", code, output, err)
	}
}
