package app

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestAuthorizeOffersExplicitAPIKeyFallbackWithoutPassword(t *testing.T) {
	input := strings.NewReader("2\nsk-test-key\n")
	var output bytes.Buffer
	r := &runner{
		ctx:    context.Background(),
		in:     input,
		reader: bufio.NewReader(input),
		out:    &output,
		errOut: &output,
	}
	key, name, err := r.authorize("https://beeapi.dev", true)
	if err != nil {
		t.Fatal(err)
	}
	if key != "sk-test-key" || name != "manual" {
		t.Fatalf("unexpected fallback result: key=%q name=%q", key, name)
	}
	text := output.String()
	if !strings.Contains(text, "跳转网站授权登录") || !strings.Contains(text, "直接粘贴 API Key") {
		t.Fatalf("login choices missing from output:\n%s", text)
	}
	if strings.Contains(text, "账户密码") {
		t.Fatalf("CLI unexpectedly asks for an account password:\n%s", text)
	}
}
