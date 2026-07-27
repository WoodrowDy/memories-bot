package awssig

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Vector from AWS's published SigV4 test suite (get-vanilla). If this passes,
// the canonical-request / string-to-sign / signing-key chain is byte-correct.
func TestSignMatchesAWSTestVector(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	creds := Creds{
		AccessKeyID:     "AKIDEXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
	}
	when := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

	if err := Sign(req, nil, "service", "us-east-1", creds, when); err != nil {
		t.Fatal(err)
	}

	want := "AWS4-HMAC-SHA256 " +
		"Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, " +
		"SignedHeaders=host;x-amz-date, " +
		"Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestSignRequiresCredentials(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://sqs.ap-northeast-2.amazonaws.com/", nil)
	if err := Sign(req, nil, "sqs", "ap-northeast-2", Creds{}, time.Now()); err == nil {
		t.Fatal("expected an error when credentials are missing")
	}
}

func TestSignIncludesSessionToken(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://sqs.ap-northeast-2.amazonaws.com/", nil)
	req.Header.Set("X-Amz-Target", "AmazonSQS.SendMessage")
	creds := Creds{AccessKeyID: "AKID", SecretAccessKey: "secret", SessionToken: "tok"}

	if err := Sign(req, []byte(`{}`), "sqs", "ap-northeast-2", creds, time.Now()); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("X-Amz-Security-Token") != "tok" {
		t.Error("session token header not set")
	}
	auth := req.Header.Get("Authorization")
	for _, h := range []string{"host", "x-amz-date", "x-amz-security-token", "x-amz-target"} {
		if !strings.Contains(auth, h) {
			t.Errorf("SignedHeaders missing %q: %s", h, auth)
		}
	}
}
