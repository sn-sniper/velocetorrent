package engine

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

// ErrCorruptBencode is returned when a .torrent or tracker payload fails strict bencode validation.
var ErrCorruptBencode = errors.New("corrupt bencode payload")

// BencodeParser decodes bencode data while tracking raw byte offsets (critical for exact info_hash calculation).
type BencodeParser struct {
	data         []byte
	pos          int
	rawInfoStart int
	rawInfoEnd   int
	hasRawInfo   bool
}

// DecodeBencode parses a bencoded byte slice into native Go types (map[string]any, []any, int64, string)
// and also extracts the exact raw bytes of the top-level "info" dictionary if present.
func DecodeBencode(data []byte) (any, []byte, error) {
	if len(data) == 0 {
		return nil, nil, fmt.Errorf("%w: empty payload", ErrCorruptBencode)
	}
	p := &BencodeParser{data: data}
	val, err := p.parseValue(true)
	if err != nil {
		return nil, nil, err
	}
	var rawInfo []byte
	if p.hasRawInfo && p.rawInfoStart >= 0 && p.rawInfoEnd <= len(data) && p.rawInfoStart < p.rawInfoEnd {
		rawInfo = data[p.rawInfoStart:p.rawInfoEnd]
	}
	return val, rawInfo, nil
}

func (p *BencodeParser) parseValue(isTopDict bool) (any, error) {
	if p.pos >= len(p.data) {
		return nil, fmt.Errorf("%w: unexpected EOF at byte %d", ErrCorruptBencode, p.pos)
	}
	switch p.data[p.pos] {
	case 'i':
		return p.parseInt()
	case 'l':
		return p.parseList()
	case 'd':
		return p.parseDict(isTopDict)
	default:
		if p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			return p.parseString()
		}
		return nil, fmt.Errorf("%w: invalid token %q at byte %d", ErrCorruptBencode, p.data[p.pos], p.pos)
	}
}

func (p *BencodeParser) parseInt() (int64, error) {
	p.pos++ // skip 'i'
	endIdx := bytes.IndexByte(p.data[p.pos:], 'e')
	if endIdx < 0 {
		return 0, fmt.Errorf("%w: unterminated integer starting at byte %d", ErrCorruptBencode, p.pos-1)
	}
	rawNum := string(p.data[p.pos : p.pos+endIdx])
	if len(rawNum) == 0 {
		return 0, fmt.Errorf("%w: empty integer at byte %d", ErrCorruptBencode, p.pos-1)
	}
	if (len(rawNum) > 1 && rawNum[0] == '0') || (len(rawNum) > 1 && rawNum[0] == '-' && rawNum[1] == '0') {
		return 0, fmt.Errorf("%w: invalid leading zero in integer %q at byte %d", ErrCorruptBencode, rawNum, p.pos-1)
	}
	val, err := strconv.ParseInt(rawNum, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid integer %q at byte %d: %v", ErrCorruptBencode, rawNum, p.pos-1, err)
	}
	p.pos += endIdx + 1
	return val, nil
}

func (p *BencodeParser) parseString() (string, error) {
	colonIdx := bytes.IndexByte(p.data[p.pos:], ':')
	if colonIdx <= 0 {
		return "", fmt.Errorf("%w: missing string length delimiter ':' at byte %d", ErrCorruptBencode, p.pos)
	}
	rawLen := string(p.data[p.pos : p.pos+colonIdx])
	strLen, err := strconv.Atoi(rawLen)
	if err != nil || strLen < 0 {
		return "", fmt.Errorf("%w: invalid string length %q at byte %d", ErrCorruptBencode, rawLen, p.pos)
	}
	p.pos += colonIdx + 1
	if p.pos+strLen > len(p.data) {
		return "", fmt.Errorf("%w: string length %d exceeds payload bounds at byte %d", ErrCorruptBencode, strLen, p.pos)
	}
	s := string(p.data[p.pos : p.pos+strLen])
	p.pos += strLen
	return s, nil
}

func (p *BencodeParser) parseList() ([]any, error) {
	p.pos++ // skip 'l'
	list := make([]any, 0)
	for {
		if p.pos >= len(p.data) {
			return nil, fmt.Errorf("%w: unterminated list before EOF", ErrCorruptBencode)
		}
		if p.data[p.pos] == 'e' {
			p.pos++
			return list, nil
		}
		item, err := p.parseValue(false)
		if err != nil {
			return nil, err
		}
		list = append(list, item)
	}
}

func (p *BencodeParser) parseDict(isTopDict bool) (map[string]any, error) {
	p.pos++ // skip 'd'
	dict := make(map[string]any)
	for {
		if p.pos >= len(p.data) {
			return nil, fmt.Errorf("%w: unterminated dictionary before EOF", ErrCorruptBencode)
		}
		if p.data[p.pos] == 'e' {
			p.pos++
			return dict, nil
		}
		key, err := p.parseString()
		if err != nil {
			return nil, fmt.Errorf("%w: invalid dictionary key: %v", ErrCorruptBencode, err)
		}
		valStart := p.pos
		val, err := p.parseValue(false)
		if err != nil {
			return nil, err
		}
		valEnd := p.pos
		if isTopDict && key == "info" && !p.hasRawInfo {
			p.rawInfoStart = valStart
			p.rawInfoEnd = valEnd
			p.hasRawInfo = true
		}
		dict[key] = val
	}
}

// EncodeBencode serializes Go values (int, int64, string, []byte, []any, map[string]any) into canonical sorted bencode.
func EncodeBencode(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := encodeValue(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeValue(buf *bytes.Buffer, v any) error {
	switch val := v.(type) {
	case int:
		fmt.Fprintf(buf, "i%de", val)
	case int64:
		fmt.Fprintf(buf, "i%de", val)
	case uint64:
		fmt.Fprintf(buf, "i%de", val)
	case string:
		fmt.Fprintf(buf, "%d:%s", len(val), val)
	case []byte:
		fmt.Fprintf(buf, "%d:", len(val))
		buf.Write(val)
	case []string:
		buf.WriteByte('l')
		for _, item := range val {
			if err := encodeValue(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte('e')
	case []any:
		buf.WriteByte('l')
		for _, item := range val {
			if err := encodeValue(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte('e')
	case map[string]any:
		buf.WriteByte('d')
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := encodeValue(buf, k); err != nil {
				return err
			}
			if err := encodeValue(buf, val[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('e')
	default:
		return fmt.Errorf("unsupported bencode type %T", v)
	}
	return nil
}
