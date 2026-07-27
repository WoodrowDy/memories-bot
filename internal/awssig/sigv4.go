// Package awssig signs plain net/http requests with AWS Signature Version 4.
//
// Why hand-rolled instead of aws-sdk-go-v2: Lambda already hands us credentials
// in the environment, and SQS SendMessage is the only AWS call this bot makes.
// One 100-line file beats ~40 transitive modules and keeps the binary small.
package awssig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const algorithm = "AWS4-HMAC-SHA256"

// Creds are the credentials the Lambda execution environment injects.
type Creds struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// CredsFromEnv reads the credentials Lambda sets on every invocation.
func CredsFromEnv() Creds {
	return Creds{
		AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
	}
}

// Sign adds Authorization (plus X-Amz-Security-Token for temporary credentials)
// to req. body must be the exact bytes that will be sent — the signature covers
// their hash, so a mismatch is rejected by AWS.
func Sign(req *http.Request, body []byte, service, region string, c Creds, now time.Time) error {
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return errors.New("awssig: no AWS credentials in environment")
	}
	if region == "" {
		return errors.New("awssig: empty region")
	}
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	// net/http sends Host from req.URL, but SigV4 requires it in the canonical
	// headers — same value either way, so the signature stays consistent.
	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Date", amzDate)
	if c.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.SessionToken)
	}

	signedHeaders, canonHeaders := canonicalHeaders(req.Header)

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL.Path),
		req.URL.RawQuery,
		canonHeaders, // already ends in \n; Join adds the blank-line separator
		signedHeaders,
		sha256Hex(body),
	}, "\n")

	scope := strings.Join([]string{dateStamp, region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		algorithm,
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	sig := hex.EncodeToString(hmacSHA256(signingKey(c.SecretAccessKey, dateStamp, region, service), stringToSign))
	req.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, c.AccessKeyID, scope, signedHeaders, sig))
	return nil
}

func canonicalHeaders(h http.Header) (signedList, canonical string) {
	names := make([]string, 0, len(h))
	values := make(map[string]string, len(h))
	for k, v := range h {
		lk := strings.ToLower(k)
		names = append(names, lk)
		parts := make([]string, len(v))
		for i, s := range v {
			parts[i] = collapseSpaces(s)
		}
		values[lk] = strings.Join(parts, ",")
	}
	sort.Strings(names)

	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte(':')
		b.WriteString(values[n])
		b.WriteByte('\n')
	}
	return strings.Join(names, ";"), b.String()
}

// collapseSpaces trims and squeezes runs of spaces, as SigV4 requires.
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func canonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	return path
}

func signingKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
