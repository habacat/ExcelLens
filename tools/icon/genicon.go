// Command genicon builds Windows COFF .syso icon and application-manifest
// resources. The Go linker includes them in the Windows EXE without a resource
// compiler; -check-exe verifies the loader-visible manifest after UPX packing.
//
// This build-time tool uses only Go's standard library and runs on Linux or
// Windows. It is not part of the distributed application runtime.
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"
)

type iconEntry struct {
	width, height, colors, reserved byte
	planes, bitCount                uint16
	image                           []byte
}

type reloc struct{ offset uint32 }

func main() {
	ico := flag.String("ico", "internal/branding/icon.ico", "source ico")
	manifestPath := flag.String("manifest", "cmd/excellens/app.manifest", "application manifest template")
	output := flag.String("o", "cmd/excellens/rsrc_windows_amd64.syso", "destination .syso")
	executable := flag.String("check-exe", "", "verify the manifest embedded in a built Windows EXE")
	flag.Parse()
	template, err := os.ReadFile(*manifestPath)
	if err != nil {
		panic(err)
	}
	manifest, err := renderManifest(template)
	if err != nil {
		panic(err)
	}
	if *executable != "" {
		if err := verifyExecutable(*executable, manifest); err != nil {
			panic(err)
		}
		fmt.Printf("verified %s: current PerMonitorV2 application manifest\n", *executable)
		return
	}
	b, err := os.ReadFile(*ico)
	if err != nil {
		panic(err)
	}
	data, relocations, err := makeResource(b, manifest)
	if err != nil {
		panic(err)
	}
	if err := writeCOFF(*output, data, relocations); err != nil {
		panic(err)
	}
	fmt.Printf("created %s: %d images, PerMonitorV2 manifest, %d-byte resources\n", *output, len(relocations)-2, len(data))
}

func parseICO(b []byte) ([]iconEntry, error) {
	if len(b) < 6 || binary.LittleEndian.Uint16(b[:2]) != 0 || binary.LittleEndian.Uint16(b[2:4]) != 1 {
		return nil, errors.New("invalid ICO header")
	}
	count := int(binary.LittleEndian.Uint16(b[4:6]))
	if count == 0 || count > 30 || len(b) < 6+16*count {
		return nil, errors.New("invalid ICO image count")
	}
	entries := make([]iconEntry, 0, count)
	for i := 0; i < count; i++ {
		x := b[6+i*16 : 6+(i+1)*16]
		size := int(binary.LittleEndian.Uint32(x[8:12]))
		off := int(binary.LittleEndian.Uint32(x[12:16]))
		if off < 0 || size < 1 || off > len(b) || size > len(b)-off {
			return nil, errors.New("invalid ICO payload")
		}
		entries = append(entries, iconEntry{width: x[0], height: x[1], colors: x[2], reserved: x[3], planes: binary.LittleEndian.Uint16(x[4:6]), bitCount: binary.LittleEndian.Uint16(x[6:8]), image: b[off : off+size]})
	}
	return entries, nil
}

func putU16(b []byte, off int, v uint16) { binary.LittleEndian.PutUint16(b[off:], v) }
func putU32(b []byte, off int, v uint32) { binary.LittleEndian.PutUint32(b[off:], v) }
func align4(n int) int                   { return (n + 3) &^ 3 }
func makeResource(source, manifest []byte) ([]byte, []reloc, error) {
	entries, err := parseICO(source)
	if err != nil {
		return nil, nil, err
	}
	if len(manifest) == 0 {
		return nil, nil, errors.New("application manifest is required")
	}
	n := len(entries)
	// Resource types, sorted by ID: RT_ICON=3, RT_GROUP_ICON=14, RT_MANIFEST=24.
	rootOff := 0
	iconTypeOff := rootOff + 16 + 3*8
	groupTypeOff := iconTypeOff + 16 + n*8
	manifestTypeOff := groupTypeOff + 16 + 8
	languageDirOff := manifestTypeOff + 16 + 8
	groupLanguageDirOff := languageDirOff + n*(16+8)
	manifestLanguageDirOff := groupLanguageDirOff + 16 + 8
	dataEntriesOff := manifestLanguageDirOff + 16 + 8
	payloadStart := align4(dataEntriesOff + (n+2)*16)
	groupSize := 6 + n*14
	groupPayloadOff := payloadStart
	manifestPayloadOff := align4(groupPayloadOff + groupSize)
	imageOffs := make([]int, n)
	next := align4(manifestPayloadOff + len(manifest))
	for i, entry := range entries {
		imageOffs[i] = next
		next = align4(next + len(entry.image))
	}
	data := make([]byte, next)
	// directory header: time/date 0, named entries 0, ID entry count.
	dir := func(off, count int) { putU16(data, off+14, uint16(count)) }
	entry := func(off, index, id, target int, isDir bool) {
		p := off + 16 + 8*index
		putU32(data, p, uint32(id))
		v := uint32(target)
		if isDir {
			v |= 0x80000000
		}
		putU32(data, p+4, v)
	}
	dir(rootOff, 3)
	entry(rootOff, 0, 3, iconTypeOff, true)
	entry(rootOff, 1, 14, groupTypeOff, true)
	entry(rootOff, 2, 24, manifestTypeOff, true)
	dir(iconTypeOff, n)
	for i := range entries {
		entry(iconTypeOff, i, i+1, languageDirOff+i*24, true)
	}
	dir(groupTypeOff, 1)
	entry(groupTypeOff, 0, 1, groupLanguageDirOff, true)
	dir(manifestTypeOff, 1)
	entry(manifestTypeOff, 0, 1, manifestLanguageDirOff, true)
	relocations := make([]reloc, 0, n+2)
	for i, e := range entries {
		lang := languageDirOff + i*24
		dir(lang, 1)
		entry(lang, 0, 1033, dataEntriesOff+i*16, false)
		dat := dataEntriesOff + i*16
		putU32(data, dat, uint32(imageOffs[i]))
		putU32(data, dat+4, uint32(len(e.image)))
		copy(data[imageOffs[i]:], e.image)
		relocations = append(relocations, reloc{offset: uint32(dat)})
	}
	dir(groupLanguageDirOff, 1)
	entry(groupLanguageDirOff, 0, 1033, dataEntriesOff+n*16, false)
	groupDataEntry := dataEntriesOff + n*16
	putU32(data, groupDataEntry, uint32(groupPayloadOff))
	putU32(data, groupDataEntry+4, uint32(groupSize))
	relocations = append(relocations, reloc{offset: uint32(groupDataEntry)})
	putU16(data, groupPayloadOff+2, 1)
	putU16(data, groupPayloadOff+4, uint16(n))
	for i, e := range entries {
		off := groupPayloadOff + 6 + i*14
		data[off] = e.width
		data[off+1] = e.height
		data[off+2] = e.colors
		data[off+3] = e.reserved
		putU16(data, off+4, e.planes)
		putU16(data, off+6, e.bitCount)
		putU32(data, off+8, uint32(len(e.image)))
		putU16(data, off+12, uint16(i+1))
	}
	dir(manifestLanguageDirOff, 1)
	entry(manifestLanguageDirOff, 0, 0, dataEntriesOff+(n+1)*16, false)
	manifestDataEntry := dataEntriesOff + (n+1)*16
	putU32(data, manifestDataEntry, uint32(manifestPayloadOff))
	putU32(data, manifestDataEntry+4, uint32(len(manifest)))
	putU32(data, manifestDataEntry+8, 65001) // UTF-8 XML, language-neutral resource
	copy(data[manifestPayloadOff:], manifest)
	relocations = append(relocations, reloc{offset: uint32(manifestDataEntry)})
	return data, relocations, nil
}

func writeCOFF(path string, rsrc []byte, relocs []reloc) error {
	if len(relocs) > 65535 {
		return errors.New("too many resource relocations")
	}
	const fileHeaderSize = 20
	const sectionHeaderSize = 40
	const symbolSize = 18
	const relocationSize = 10
	sectionStart := fileHeaderSize + sectionHeaderSize
	relocStart := sectionStart + len(rsrc)
	symbolStart := relocStart + len(relocs)*relocationSize
	buffer := make([]byte, symbolStart+symbolSize+4)
	// COFF machine AMD64 0x8664; one .rsrc section, one static symbol.
	putU16(buffer, 0, 0x8664)
	putU16(buffer, 2, 1)
	putU32(buffer, 8, uint32(symbolStart))
	putU32(buffer, 12, 1)
	name := buffer[fileHeaderSize : fileHeaderSize+8]
	copy(name, []byte(".rsrc"))
	sec := fileHeaderSize
	putU32(buffer, sec+16, uint32(len(rsrc)))
	putU32(buffer, sec+20, uint32(sectionStart))
	putU32(buffer, sec+24, uint32(relocStart))
	putU16(buffer, sec+32, uint16(len(relocs)))
	putU32(buffer, sec+36, 0x40000040) // read-only initialized data
	copy(buffer[sectionStart:], rsrc)
	for i, r := range relocs {
		off := relocStart + i*relocationSize
		putU32(buffer, off, r.offset)
		putU32(buffer, off+4, 0)      // section symbol index 0
		putU16(buffer, off+8, 0x0003) // IMAGE_REL_AMD64_ADDR32NB
	}
	copy(buffer[symbolStart:], []byte(".rsrc"))
	putU16(buffer, symbolStart+12, 1)         // section number
	buffer[symbolStart+16] = 3                // IMAGE_SYM_CLASS_STATIC
	putU32(buffer, symbolStart+symbolSize, 4) // empty COFF string table
	return os.WriteFile(path, buffer, 0644)
}
