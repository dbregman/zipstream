package zipstream

import (
	"archive/zip"
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

// We're not interested in the central directories data, we just want to skip over it,
// clearing the stream of the current zip, in case anything needs to be sent over the
// same stream.
func discardCentralDirectory(br *bufio.Reader) error {
	for {
		sigBytes, err := br.Peek(4)
		if err != nil {
			return err
		}
		switch sig := binary.LittleEndian.Uint32(sigBytes); sig {
		case directoryHeaderSignature:
			if err := discardDirectoryHeaderRecord(br); err != nil {
				return err
			}
		case directoryEndSignature:
			if err := discardDirectoryEndRecord(br); err != nil {
				return err
			}
			return io.EOF
		case directory64EndSignature:
			if err := discardDirectory64End(br); err != nil {
				return err
			}
		case directory64LocSignature:
			if err := discardDirectory64EndLocator(br); err != nil {
				return err
			}
		default:
			return zip.ErrFormat
		}
	}
}

func discardDirectoryHeaderRecord(br *bufio.Reader) error {
	if _, err := br.Discard(28); err != nil {
		return err
	}
	lb, err := br.Peek(6)
	if err != nil {
		return err
	}
	lengths := int(binary.LittleEndian.Uint16(lb[:2])) + // File name length
		int(binary.LittleEndian.Uint16(lb[2:4])) + // Extra field length
		int(binary.LittleEndian.Uint16(lb[4:])) // File comment length
	_, err = br.Discard(18 + lengths)
	return err
}

func discardDirectoryEndRecord(br *bufio.Reader) error {
	if _, err := br.Discard(20); err != nil {
		return err
	}
	commentLength, err := br.Peek(2)
	if err != nil {
		return err
	}
	_, err = br.Discard(2 + int(binary.LittleEndian.Uint16(commentLength)))
	return err
}

func discardDirectory64End(br *bufio.Reader) error {
	lb, err := br.Peek(12)
	if err != nil {
		return err
	}
	totalSize := 12 + binary.LittleEndian.Uint64(lb[4:])
	if totalSize > 0x7FFFFFFF {
		return errors.New("discardDirectory64End: size overflow")
	}
	_, err = br.Discard(int(totalSize))
	return err
}

func discardDirectory64EndLocator(br *bufio.Reader) error {
	_, err := br.Discard(20)
	return err
}
