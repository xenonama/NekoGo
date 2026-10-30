// Command png2ico converts a PNG into a single-image ICO file using
// PNG-compression (supported since Windows Vista). Standard library only.
//
// Usage:
//
//	go run ./script/png2ico.go -in gui/frontend/src/assets/app.png -out gui/app.ico
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image/png"
	"os"
)

func main() {
	in := flag.String("in", "", "input PNG path")
	out := flag.String("out", "", "output ICO path")
	flag.Parse()
	if err := run(*in, *out); err != nil {
		fmt.Fprintln(os.Stderr, "png2ico:", err)
		os.Exit(1)
	}
}

func run(in, out string) error {
	if in == "" || out == "" {
		return fmt.Errorf("both -in and -out are required")
	}
	raw, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("decode PNG: %w", err)
	}
	if cfg.Width > 256 || cfg.Height > 256 {
		return fmt.Errorf("PNG is %dx%d, expected at most 256x256", cfg.Width, cfg.Height)
	}
	dim := func(n int) byte {
		if n >= 256 {
			return 0 // 0 means 256 in ICO headers
		}
		return byte(n)
	}
	buf := new(bytes.Buffer)
	// ICONDIR
	buf.Write([]byte{0, 0, 1, 0, 1, 0})
	// ICONDIRENTRY
	buf.Write([]byte{dim(cfg.Width), dim(cfg.Height), 0, 0})
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))   // planes
	_ = binary.Write(buf, binary.LittleEndian, uint16(32))  // bit count
	_ = binary.Write(buf, binary.LittleEndian, uint32(len(raw)))
	_ = binary.Write(buf, binary.LittleEndian, uint32(6+16)) // image offset
	buf.Write(raw)
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Printf("png2ico: %s (%dx%d) -> %s (%d bytes)\n", in, cfg.Width, cfg.Height, out, buf.Len())
	return nil
}
