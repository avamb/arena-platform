// signature.go — verifying the `hash` header of a PayPlus callback.
//
// PayPlus signs a callback with HMAC-SHA256 under the merchant's SECRET KEY
// and sends base64(digest) in the `hash` header (docs: "Validate Requests
// Received from PayPlus"). What the digest covers is the open question: the
// documented Node.js sample hashes JSON.stringify(req.body), i.e. the body
// PARSED and re-serialized, not the bytes on the wire. When PayPlus itself
// serializes with JSON.stringify and sends that string, the two are the same
// bytes; when anything between (a pretty-printer, a different serializer)
// changes whitespace or escapes, only a re-serialization matches.
//
// VerifyCallback therefore tries, in this order, and accepts the first match
// (constant-time compare, hmac.Equal, for every candidate):
//
//  1. the raw body bytes exactly as received;
//  2. the raw bytes with insignificant whitespace removed (json.Compact keeps
//     key order and every escape as sent);
//  3. a JSON.stringify-equivalent re-serialization: key order as received,
//     no whitespace, strings escaped the way JavaScript escapes them (only
//     `"`, `\` and control characters; `/`, `<`, `>`, `&` and non-ASCII are
//     literal), numbers printed the way JavaScript prints them.
//
// All three are deterministic functions of the received body; none of them
// lets a body that was not signed by the key pass. Which one real callbacks
// match must be confirmed on restapidev with real test keys
// (docs/ops/payplus.md); if it is always (1), the fallbacks can go.
package payplus

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// HeaderHash is the header that carries the signature.
const HeaderHash = "hash"

// ErrInvalidSignature is returned when a callback does not verify.
var ErrInvalidSignature = fmt.Errorf("%w: payplus hash mismatch", payments.ErrInvalidWebhookSignature)

// Sign returns base64(HMAC-SHA256(secretKey, message)), the value of the
// `hash` header PayPlus sends for message.
func Sign(secretKey string, message []byte) string {
	mac := hmac.New(sha256.New, []byte(secretKey))
	_, _ = mac.Write(message)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// SignedVariant names which bytes a verified callback was signed over.
type SignedVariant string

const (
	VariantRaw         SignedVariant = "raw"
	VariantCompact     SignedVariant = "compact"
	VariantJSStringify SignedVariant = "js_stringify"
)

// VerifyHash checks hashHeader against the body under secretKey and reports
// which byte form matched. An empty key or header never verifies.
func VerifyHash(body []byte, hashHeader, secretKey string) (SignedVariant, error) {
	got, err := base64.StdEncoding.DecodeString(strings.TrimSpace(hashHeader))
	if err != nil || len(got) == 0 || secretKey == "" {
		return "", ErrInvalidSignature
	}
	check := func(msg []byte) bool {
		mac := hmac.New(sha256.New, []byte(secretKey))
		_, _ = mac.Write(msg)
		return hmac.Equal(mac.Sum(nil), got)
	}
	if check(body) {
		return VariantRaw, nil
	}
	var compact bytes.Buffer
	if json.Compact(&compact, body) == nil && !bytes.Equal(compact.Bytes(), body) && check(compact.Bytes()) {
		return VariantCompact, nil
	}
	if js, jsErr := JSStringify(body); jsErr == nil && check(js) {
		return VariantJSStringify, nil
	}
	return "", ErrInvalidSignature
}

// JSStringify re-serializes a JSON document the way JavaScript's
// JSON.stringify(JSON.parse(body)) would: object keys in the order received
// (a duplicate key keeps its FIRST position and its LAST value, as in JS),
// no whitespace, JS string escaping and JS number formatting.
func JSStringify(body []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	v, err := readValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("payplus: trailing data after the JSON value")
	}
	var buf bytes.Buffer
	writeValue(&buf, v)
	return buf.Bytes(), nil
}

// orderedObject keeps keys in first-seen order.
type orderedObject struct {
	keys []string
	vals map[string]any
}

func readValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := &orderedObject{vals: map[string]any{}}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, errors.New("payplus: object key is not a string")
				}
				v, err := readValue(dec)
				if err != nil {
					return nil, err
				}
				if _, seen := obj.vals[k]; !seen {
					obj.keys = append(obj.keys, k)
				}
				obj.vals[k] = v
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := readValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, errors.New("payplus: unexpected delimiter")
	default:
		return t, nil
	}
}

func writeValue(buf *bytes.Buffer, v any) {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		buf.WriteString(strconv.FormatBool(t))
	case json.Number:
		buf.WriteString(jsNumber(t))
	case string:
		writeJSString(buf, t)
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeValue(buf, e)
		}
		buf.WriteByte(']')
	case *orderedObject:
		buf.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeJSString(buf, k)
			buf.WriteByte(':')
			writeValue(buf, t.vals[k])
		}
		buf.WriteByte('}')
	}
}

// jsNumber prints a number as JavaScript's Number.prototype.toString does
// for the values a callback carries: 1.0 → 1, 1.50 → 1.5, 1e2 → 100.
// Exponent form for |x| >= 1e21 or < 1e-6, as in JS.
func jsNumber(n json.Number) string {
	f, err := strconv.ParseFloat(n.String(), 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return "null"
	}
	if f == 0 {
		return "0"
	}
	abs := math.Abs(f)
	if abs >= 1e21 || abs < 1e-6 {
		s := strconv.FormatFloat(f, 'e', -1, 64)
		// Go prints 1e+21 / 1e-07; JS prints 1e+21 / 1e-7.
		mant, exp, _ := strings.Cut(s, "e")
		sign := exp[:1]
		exp = strings.TrimLeft(exp[1:], "0")
		if exp == "" {
			exp = "0"
		}
		return mant + "e" + sign + exp
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// writeJSString escapes like JSON.stringify: `"` and `\` are escaped, the
// control characters get their short escapes or \u00XX, everything else
// (including `/`, HTML characters and all non-ASCII) is written literally.
func writeJSString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '"':
			buf.WriteString(`\"`)
		case r == '\\':
			buf.WriteString(`\\`)
		case r == '\b':
			buf.WriteString(`\b`)
		case r == '\f':
			buf.WriteString(`\f`)
		case r == '\n':
			buf.WriteString(`\n`)
		case r == '\r':
			buf.WriteString(`\r`)
		case r == '\t':
			buf.WriteString(`\t`)
		case r < 0x20:
			fmt.Fprintf(buf, `\u%04x`, r)
		default:
			buf.WriteString(s[i : i+size])
		}
		i += size
	}
	buf.WriteByte('"')
}
