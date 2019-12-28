// Package zipstream provides support for reading ZIP archives through an io.Reader.
package zipstream

import (
	"archive/zip"
	"bufio"
	"encoding/binary"
	"hash/crc32"
	"io"
	"io/ioutil"
	"time"
)

const (
	readAhead  = 28
	maxRead    = 4096
	bufferSize = maxRead + readAhead
)

// A Reader provides sequential access to the contents of a zip archive.
// A zip archive consists of a sequence of files,
// The Next method advances to the next file in the archive (including the first),
// and then it can be treated as an io.Reader to access the file's data.
// The Buffered method recovers any bytes read beyond the end of the zip file,
// necessary if you plan to process anything after it that is not another zip file.
type Reader struct {
	io.Reader
	br            *bufio.Reader
	decompressors map[uint16]Decompressor
}

// NewReader creates a new Reader reading from r.
func NewReader(r io.Reader) *Reader {
	return &Reader{br: bufio.NewReaderSize(r, bufferSize)}
}

// Buffered returns any bytes beyond the end of the zip file that it may have
// read. These are necessary if you plan to process anything after it,
// that isn't another zip file.
func (r *Reader) Buffered() io.Reader { return r.br }

// RegisterDecompressor registers or overrides a custom decompressor for a
// specific method ID. If a decompressor for a given method is not found,
// Reader will default to looking up the decompressor at the package level.
func (r *Reader) RegisterDecompressor(method uint16, dcomp Decompressor) {
	if r.decompressors == nil {
		r.decompressors = make(map[uint16]Decompressor)
	}
	r.decompressors[method] = dcomp
}

func (r *Reader) decompressor(method uint16) Decompressor {
	var dcomp Decompressor
	if r.decompressors != nil {
		dcomp = r.decompressors[method]
	}
	if dcomp == nil {
		dcomp = decompressor(method)
	}
	return dcomp
}

// Next advances to the next entry in the zip archive.
//
// io.EOF is returned when the end of the zip file has been reached.
// If Next is called again, it will presume another zip file immediately follows
// and it will advance into it.
func (r *Reader) Next() (*zip.FileHeader, error) {
	if r.Reader != nil {
		if _, err := io.Copy(ioutil.Discard, r.Reader); err != nil {
			return nil, err
		}
	}
	sigBytes, err := r.br.Peek(4)
	if err != nil {
		return nil, err
	}

	switch sig := binary.LittleEndian.Uint32(sigBytes); sig {
	case fileHeaderSignature:
		break
	case directoryHeaderSignature: // Directory appears at end of file so we are finished
		return nil, discardCentralDirectory(r.br)
	default:
		return nil, zip.ErrFormat
	}

	f, err := readFileHeader(r.br)
	if err != nil {
		return nil, err
	}

	dcomp := r.decompressor(f.Method)
	if dcomp == nil {
		return nil, zip.ErrAlgorithm
	}

	crc := &crcReader{
		hash: crc32.NewIEEE(),
		crc:  &f.CRC32,
	}
	if f.Flags&0x8 != 0 { // If has dataDescriptor
		crc.Reader = dcomp(&descriptorReader{br: r.br, fileHeader: f})
	} else {
		crc.Reader = dcomp(io.LimitReader(r.br, int64(f.CompressedSize64)))
		crc.crc = &f.CRC32
	}
	r.Reader = crc
	return f, nil
}

func readFileHeader(r io.Reader) (*zip.FileHeader, error) {
	var buf [fileHeaderLen]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return nil, err
	}
	b := readBuf(buf[:])
	if sig := b.uint32(); sig != fileHeaderSignature {
		return nil, zip.ErrFormat
	}
	f := &zip.FileHeader{
		ReaderVersion:    b.uint16(),
		Flags:            b.uint16(),
		Method:           b.uint16(),
		ModifiedTime:     b.uint16(),
		ModifiedDate:     b.uint16(),
		CRC32:            b.uint32(),
		CompressedSize:   b.uint32(),
		UncompressedSize: b.uint32(),
	}
	f.CompressedSize64 = uint64(f.CompressedSize)
	f.UncompressedSize64 = uint64(f.UncompressedSize)
	filenameLen := int(b.uint16())
	extraLen := int(b.uint16())
	d := make([]byte, filenameLen+extraLen)
	if _, err := io.ReadFull(r, d); err != nil {
		return nil, err
	}
	f.Name = string(d[:filenameLen])
	f.Extra = d[filenameLen : filenameLen+extraLen]

	// Determine the character encoding.
	utf8Valid1, utf8Require1 := detectUTF8(f.Name)
	utf8Valid2, utf8Require2 := detectUTF8(f.Comment)
	switch {
	case !utf8Valid1 || !utf8Valid2:
		// Name and Comment definitely not UTF-8.
		f.NonUTF8 = true
	case !utf8Require1 && !utf8Require2:
		// Name and Comment use only single-byte runes that overlap with UTF-8.
		f.NonUTF8 = false
	default:
		// Might be UTF-8, might be some other encoding; preserve existing flag.
		// Some ZIP writers use UTF-8 encoding without setting the UTF-8 flag.
		// Since it is impossible to always distinguish valid UTF-8 from some
		// other encoding (e.g., GBK or Shift-JIS), we trust the flag.
		f.NonUTF8 = f.Flags&0x800 == 0
	}

	needUSize := f.UncompressedSize == ^uint32(0)
	needCSize := f.CompressedSize == ^uint32(0)

	// Best effort to find what we need.
	// Other zip authors might not even follow the basic format,
	// and we'll just ignore the Extra content in that case.
	var modified time.Time
parseExtras:
	for extra := readBuf(f.Extra); len(extra) >= 4; { // need at least tag and size
		fieldTag := extra.uint16()
		fieldSize := int(extra.uint16())
		if len(extra) < fieldSize {
			break
		}
		fieldBuf := extra.sub(fieldSize)

		switch fieldTag {
		case zip64ExtraID:
			// update directory values from the zip64 extra block.
			// They should only be consulted if the sizes read earlier
			// are maxed out.
			// See golang.org/issue/13367.
			if needUSize {
				needUSize = false
				if len(fieldBuf) < 8 {
					return nil, zip.ErrFormat
				}
				f.UncompressedSize64 = fieldBuf.uint64()
			}
			if needCSize {
				needCSize = false
				if len(fieldBuf) < 8 {
					return nil, zip.ErrFormat
				}
				f.CompressedSize64 = fieldBuf.uint64()
			}
		case ntfsExtraID:
			if len(fieldBuf) < 4 {
				continue parseExtras
			}
			fieldBuf.uint32()        // reserved (ignored)
			for len(fieldBuf) >= 4 { // need at least tag and size
				attrTag := fieldBuf.uint16()
				attrSize := int(fieldBuf.uint16())
				if len(fieldBuf) < attrSize {
					continue parseExtras
				}
				attrBuf := fieldBuf.sub(attrSize)
				if attrTag != 1 || attrSize != 24 {
					continue // Ignore irrelevant attributes
				}

				const ticksPerSecond = 1e7    // Windows timestamp resolution
				ts := int64(attrBuf.uint64()) // ModTime since Windows epoch
				secs := int64(ts / ticksPerSecond)
				nsecs := (1e9 / ticksPerSecond) * int64(ts%ticksPerSecond)
				epoch := time.Date(1601, time.January, 1, 0, 0, 0, 0, time.UTC)
				modified = time.Unix(epoch.Unix()+secs, nsecs)
			}
		case unixExtraID, infoZipUnixExtraID:
			if len(fieldBuf) < 8 {
				continue parseExtras
			}
			fieldBuf.uint32()              // AcTime (ignored)
			ts := int64(fieldBuf.uint32()) // ModTime since Unix epoch
			modified = time.Unix(ts, 0)
		case extTimeExtraID:
			if len(fieldBuf) < 5 || fieldBuf.uint8()&1 == 0 {
				continue parseExtras
			}
			ts := int64(fieldBuf.uint32()) // ModTime since Unix epoch
			modified = time.Unix(ts, 0)
		}
	}

	msdosModified := msDosTimeToTime(f.ModifiedDate, f.ModifiedTime)
	f.Modified = msdosModified
	if !modified.IsZero() {
		f.Modified = modified.UTC()

		// If legacy MS-DOS timestamps are set, we can use the delta between
		// the legacy and extended versions to estimate timezone offset.
		//
		// A non-UTC timezone is always used (even if offset is zero).
		// Thus, FileHeader.Modified.Location() == time.UTC is useful for
		// determining whether extended timestamps are present.
		// This is necessary for users that need to do additional time
		// calculations when dealing with legacy ZIP formats.
		if f.ModifiedTime != 0 || f.ModifiedDate != 0 {
			f.Modified = modified.In(timeZone(msdosModified.Sub(modified)))
		}
	}

	// Assume that uncompressed size 2³²-1 could plausibly happen in
	// an old zip32 file that was sharding inputs into the largest chunks
	// possible (or is just malicious; search the web for 42.zip).
	// If needUSize is true still, it means we didn't see a zip64 extension.
	// As long as the compressed size is not also 2³²-1 (implausible)
	// and the header is not also 2³²-1 (equally implausible),
	// accept the uncompressed size 2³²-1 as valid.
	// If nothing else, this keeps archive/zip working with 42.zip.
	_ = needUSize

	if needCSize {
		return nil, zip.ErrFormat
	}

	return f, nil
}
