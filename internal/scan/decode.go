package scan

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
)

// maxDecodedBytes bounds the bytes of text that checking one result may
// build by decoding runs nested in one another, so that text with many such
// runs cannot make the check run for long or hold much memory. A result
// whose check runs out is not placed.
const maxDecodedBytes = 16 << 20

// minNestedRun is the length in bytes of the shortest run, inside text that
// was already decoded, that the check decodes again. Shorter runs are mostly
// words.
const minNestedRun = 8

// encodedSpan returns the range of the encoded text that holds the decoded
// value of raw, which a scanner reported at byte offset start of text. ok is
// false when the text there does not decode to a text holding the value.
//
// For base64 and hex the range is the whole run of that encoding's
// characters from start to the end of the run or the line: the value is in
// it only in encoded form, so no smaller range holds it. The value must be in
// the decoded run followed by the rest of the line, since a scanner reads
// the decoded run in its line, but not in the rest of the line alone. When
// raw.Depth is more than one, runs of base64 or hex in the decoded text are
// decoded again, up to raw.Depth times in all.
//
// For percent encoding the range is the part of the encoded text from start
// to the next space, quote, '<' or '>' that decodes to the value, and it must
// hold at least one escape. Percent encoding nested in another is not
// placed.
func encodedSpan(text []byte, start int, raw Raw) (_ finding.Span, ok bool) {
	secret := []byte(raw.Value)
	if len(secret) == 0 || raw.Depth < 1 || start < 0 || start >= len(text) {
		return finding.Span{}, false
	}
	lineEnd := len(text)
	if i := bytes.IndexByte(text[start:], '\n'); i >= 0 {
		lineEnd = start + i
	}
	line := text[:lineEnd]
	switch raw.Encoding {
	case finding.EncodingBase64, finding.EncodingHex:
		end := runEnd(line, start, raw.Encoding)
		decoded, ok := decodeRun(raw.Encoding, line[start:end])
		if !ok {
			return finding.Span{}, false
		}
		rest := line[end:]
		if bytes.Contains(rest, secret) {
			return finding.Span{}, false
		}
		budget := maxDecodedBytes
		if !holds(append(decoded, rest...), secret, raw.Depth-1, &budget) {
			return finding.Span{}, false
		}
		return finding.Span{Start: start, End: end}, true
	case finding.EncodingPercent:
		if raw.Depth != 1 {
			return finding.Span{}, false
		}
		return percentSpan(line, start, secret)
	}
	return finding.Span{}, false
}

// runEnd returns the end of the run of encoding's characters that starts at
// start in text. A base64 run takes at most two '=' of padding at its end.
func runEnd(text []byte, start int, encoding finding.Encoding) int {
	end := start
	if encoding == finding.EncodingHex {
		for end < len(text) && isHexDigit(text[end]) {
			end++
		}
		return end
	}
	for end < len(text) && isBase64Char(text[end]) {
		end++
	}
	for pad := 0; pad < 2 && end < len(text) && text[end] == '='; pad++ {
		end++
	}
	return end
}

// decodeRun decodes run. Base64 may use the standard or the URL alphabet,
// with or without padding. ok is false for an empty run or one that does not
// decode.
func decodeRun(encoding finding.Encoding, run []byte) (_ []byte, ok bool) {
	if len(run) == 0 {
		return nil, false
	}
	if encoding == finding.EncodingHex {
		out := make([]byte, hex.DecodedLen(len(run)))
		if _, err := hex.Decode(out, run); err != nil {
			return nil, false
		}
		return out, true
	}
	enc := base64.RawStdEncoding
	if bytes.ContainsAny(run, "-_") {
		enc = base64.RawURLEncoding
	}
	if run[len(run)-1] == '=' {
		enc = enc.WithPadding(base64.StdPadding)
	}
	out := make([]byte, enc.DecodedLen(len(run)))
	n, err := enc.Decode(out, run)
	if err != nil {
		return nil, false
	}
	return out[:n], true
}

// holds reports whether text holds secret, or does once runs of base64 or
// hex in it are decoded, at most depth times in a row. budget is the number
// of bytes of decoded text that may still be built; when it runs out, holds
// reports false.
func holds(text, secret []byte, depth int, budget *int) bool {
	if bytes.Contains(text, secret) {
		return true
	}
	if depth == 0 {
		return false
	}
	for i := 0; i < len(text); {
		if !isBase64Char(text[i]) {
			i++
			continue
		}
		end := runEnd(text, i, finding.EncodingBase64)
		if end-i >= minNestedRun {
			for _, encoding := range []finding.Encoding{finding.EncodingBase64, finding.EncodingHex} {
				if encoding == finding.EncodingHex && runEnd(text, i, finding.EncodingHex) != end {
					continue
				}
				decoded, ok := decodeRun(encoding, text[i:end])
				if !ok {
					continue
				}
				size := i + len(decoded) + len(text) - end
				if *budget -= size; *budget < 0 {
					return false
				}
				spliced := make([]byte, 0, size)
				spliced = append(append(append(spliced, text[:i]...), decoded...), text[end:]...)
				if holds(spliced, secret, depth-1, budget) {
					return true
				}
				if *budget < 0 {
					return false
				}
			}
		}
		i = end
	}
	return false
}

// percentSpan returns the range of the percent-encoded text, in the token of
// line that starts at start, that decodes to the first appearance of secret
// holding an escape.
func percentSpan(line []byte, start int, secret []byte) (_ finding.Span, ok bool) {
	end := start
	for end < len(line) && !isSpace(line[end]) && !isQuote(line[end]) && line[end] != '<' && line[end] != '>' {
		end++
	}
	// decoded[k] came from the encoded bytes from[k] to from[k+1].
	var decoded []byte
	var from []int
	for i := start; i < end; {
		from = append(from, i)
		if line[i] == '%' && i+2 < end && isHexDigit(line[i+1]) && isHexDigit(line[i+2]) {
			decoded = append(decoded, unhex(line[i+1])<<4|unhex(line[i+2]))
			i += 3
			continue
		}
		decoded = append(decoded, line[i])
		i++
	}
	from = append(from, end)
	for k := 0; k+len(secret) <= len(decoded); k++ {
		if !bytes.Equal(decoded[k:k+len(secret)], secret) {
			continue
		}
		s, e := from[k], from[k+len(secret)]
		if e-s > len(secret) {
			return finding.Span{Start: s, End: e}, true
		}
	}
	return finding.Span{}, false
}

func isBase64Char(b byte) bool {
	return isAlnum(b) || b == '+' || b == '/' || b == '-' || b == '_'
}

// unhex returns the value of the hex digit b.
func unhex(b byte) byte {
	switch {
	case b >= 'a':
		return b - 'a' + 10
	case b >= 'A':
		return b - 'A' + 10
	}
	return b - '0'
}

func isHexDigit(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}
