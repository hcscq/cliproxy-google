// Command extract-embed recovers files stored in a Go //go:embed embed.FS from a
// compiled linux/amd64 ELF binary. It is used to vendor the upstream Home
// management panel from a pinned, checksum-verified CLIProxyAPIHome release,
// because the panel's source repository is not public.
//
// Usage: extract-embed -bin <binary> -prefix static/ -out <dir> [-require management.html]
package main

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type section struct {
	addr uint64
	data []byte
}

func main() {
	bin := flag.String("bin", "", "ELF binary")
	prefix := flag.String("prefix", "static/", "embedded path prefix to extract")
	out := flag.String("out", "", "output directory")
	require := flag.String("require", "", "file (relative to prefix) that must be present")
	flag.Parse()
	if *bin == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	f, err := elf.Open(*bin)
	if err != nil {
		fatal(err)
	}
	var secs []section
	for _, s := range f.Sections {
		if s.Type != elf.SHT_PROGBITS || s.Addr == 0 {
			continue
		}
		d, err := s.Data()
		if err != nil {
			continue
		}
		secs = append(secs, section{s.Addr, d})
	}
	read := func(addr, n uint64) ([]byte, bool) {
		for _, s := range secs {
			end := s.addr + uint64(len(s.data))
			if addr >= s.addr && addr < end && n <= end-addr {
				return s.data[addr-s.addr : addr-s.addr+n], true
			}
		}
		return nil, false
	}
	// An embed.FS file entry is {name string; data string; hash [16]byte} = 48 bytes.
	// Scan every 8-byte aligned position for a plausible entry whose name starts with prefix.
	type entry struct {
		name string
		data []byte
	}
	found := map[string][]byte{}
	for _, s := range secs {
		d := s.data
		for off := 0; off+48 <= len(d); off += 8 {
			np := binary.LittleEndian.Uint64(d[off:])
			nl := binary.LittleEndian.Uint64(d[off+8:])
			if nl == 0 || nl > 512 {
				continue
			}
			name, ok := read(np, nl)
			if !ok || !bytes.HasPrefix(name, []byte(*prefix)) {
				continue
			}
			dp := binary.LittleEndian.Uint64(d[off+16:])
			dl := binary.LittleEndian.Uint64(d[off+24:])
			n := string(name)
			if strings.HasSuffix(n, "/") {
				continue // directory entry
			}
			if dl > 64<<20 {
				continue
			}
			var data []byte
			if dl > 0 {
				data, ok = read(dp, dl)
				if !ok {
					continue
				}
			}
			if prev, dup := found[n]; dup && len(prev) >= len(data) {
				continue
			}
			found[n] = append([]byte(nil), data...)
		}
	}
	if len(found) == 0 {
		fatal(fmt.Errorf("no embedded files with prefix %q found", *prefix))
	}
	for name, data := range found {
		rel := strings.TrimPrefix(name, *prefix)
		if rel == "" || strings.Contains(rel, "..") {
			continue
		}
		dst := filepath.Join(*out, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			fatal(err)
		}
	}
	fmt.Printf("extracted %d files\n", len(found))
	if *require != "" {
		if st, err := os.Stat(filepath.Join(*out, *require)); err != nil || st.Size() == 0 {
			fatal(fmt.Errorf("required file %s missing", *require))
		}
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "extract-embed:", err)
	os.Exit(1)
}
