package redact

import (
	"strings"
	"testing"
)

func TestTextMasksSecretValuesAndKeepsOrdinaryText(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"url", "https://user:password@example.test/a", "https://[redacted]@example.test/a"},
		{"user", `curl -u "user:password" --user=other:secret`, `curl -u "[redacted]" --user=[redacted]`},
		{"json", `{"password": "a\"b", "api_key":"value", "name":"ok"}`, `{"password": "[redacted]", "api_key":"[redacted]", "name":"ok"}`},
		{"headers", "Authorization: Bearer whatever\nx-api-key: abc\nCookie: a=b; c=d", "Authorization: [redacted]\nx-api-key: [redacted]\nCookie: [redacted]"},
		{"bearer", "use Bearer abc.def here", "use Bearer [redacted] here"},
		{"openai", "sk-test-12345", "[redacted]"},
		{"github", "ghp_abc123 github_pat_abc123", "[redacted] [redacted]"},
		{"github token prefixes", "gho_fake ghs_fake ghu_fake ghr_fake", "[redacted] [redacted] [redacted] [redacted]"},
		{"glued names", "PGPASSWORD=x APIKEY=x DBPASSWD=x APITOKEN=x APPSECRET=x MYSQL_PWD=x DB_PASSWD=x", "PGPASSWORD=[redacted] APIKEY=[redacted] DBPASSWD=[redacted] APITOKEN=[redacted] APPSECRET=[redacted] MYSQL_PWD=[redacted] DB_PASSWD=[redacted]"},
		{"glued json names", `{"PGPASSWORD":"x","APIKEY":"x","DBPASSWD":"x","MYSQL_PWD":"x"}`, `{"PGPASSWORD":"[redacted]","APIKEY":"[redacted]","DBPASSWD":"[redacted]","MYSQL_PWD":"[redacted]"}`},
		{"ordinary uppercase names", "PWD=/home/x KEYBOARD=us MONKEY=1", "PWD=/home/x KEYBOARD=us MONKEY=1"},
		{"slack", "xoxb-123-abc", "[redacted]"},
		{"aws", "AKIAIOSFODNN7EXAMPLE", "[redacted]"},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.signature", "[redacted]"},
		{"env", `API_KEY=abc TOKEN='two words' SECRET="three words" PASSWORD=p AUTH=a CREDENTIALS=c COOKIE=k SESSION=s`, `API_KEY=[redacted] TOKEN='[redacted]' SECRET="[redacted]" PASSWORD=[redacted] AUTH=[redacted] CREDENTIALS=[redacted] COOKIE=[redacted] SESSION=[redacted]`},
		{"flags", `--password abc --token="two words" --api-key 'three words'`, `--password [redacted] --token="[redacted]" --api-key '[redacted]'`},
		{"escaped env", `{"command":"API_KEY=\"two words\" cmd"}`, `{"command":"API_KEY=\"[redacted]\" cmd"}`},
		{"escaped flag", `{"command":"cmd --token \"two words\""}`, `{"command":"cmd --token \"[redacted]\""}`},
		{"adjacent tokens", "ghp_abc,ghp_def sk-abc sk-def", "[redacted],[redacted] [redacted] [redacted]"},
		{"idempotent", "API_KEY=[redacted]", "API_KEY=[redacted]"},

		{"url user only", "https://user@example.test", "https://[redacted]@example.test"},
		{"url token as user", "https://ghp_abc123@github.com/x", "https://[redacted]@github.com/x"},
		{"ssh git user", "ssh://git@github.com/x git+ssh://git@example.test/y", "ssh://git@github.com/x git+ssh://git@example.test/y"},
		{"scp git user", "git@github.com:x/y.git", "git@github.com:x/y.git"},
		{"git user with password", "https://git:password@example.test/a", "https://[redacted]@example.test/a"},
		{"git-like user", "ssh://gitlab@example.test/a ssh://GIT@example.test/b", "ssh://[redacted]@example.test/a ssh://[redacted]@example.test/b"},
		{"user escaped", `{"command":"curl -u \"user:password\""}`, `{"command":"curl -u \"[redacted]\""}`},
		{"empty and ordinary", "", ""},
		{"ordinary url port", "https://example.test:443/path", "https://example.test:443/path"},

		{"unquoted escapes", `API_KEY=two\ words cmd --password a\b`, `API_KEY=[redacted] cmd --password [redacted]`},

		{"ordinary flags", "--max-tokens=400 --auth-type=basic --cookie-jar=f", "--max-tokens=400 --auth-type=basic --cookie-jar=f"},
		{"ordinary names", "authors=x monkey=1 keyboard=us TURKEY=1 hotkey=ctrl+k BYPASS=1 COMPASS=1", "authors=x monkey=1 keyboard=us TURKEY=1 hotkey=ctrl+k BYPASS=1 COMPASS=1"},
		{"ordinary json names", `{"authors":"x","monkey":"1","keyboard":"us","sortKey":"id"}`, `{"authors":"x","monkey":"1","keyboard":"us","sortKey":"id"}`},
		{"separated pass", "DB_PASS=fake SMTP_PASS=fake X-KEY=fake", "DB_PASS=" + hidden + " SMTP_PASS=" + hidden + " X-KEY=" + hidden},
		{"name segments", "API_KEY=abc my-token=value SESSION_ID=abc", "API_KEY=[redacted] my-token=[redacted] SESSION_ID=[redacted]"},
		{"auth flag", "--auth basic", "--auth [redacted]"},

		{"private key", "a\n-----BEGIN OPENSSH PRIVATE KEY-----\nb3Blbn\n-----END OPENSSH PRIVATE KEY-----\nz", "a\n-----BEGIN OPENSSH PRIVATE KEY-----[redacted]-----END OPENSSH PRIVATE KEY-----\nz"},
		{"private key cut short", "-----BEGIN RSA PRIVATE KEY-----\nMIIE", "-----BEGIN RSA PRIVATE KEY-----[redacted]"},
		{"public key", "-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----", "-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----"},
		{"unchanged", "feature/sk-add-user-avatar 0123456789abcdef0123456789abcdef01234567 -p https://example.test/a NAME=value", "feature/sk-add-user-avatar 0123456789abcdef0123456789abcdef01234567 -p https://example.test/a NAME=value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Text(tc.in); got != tc.want {
				t.Fatalf("Text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTextMasksLongTextWithoutCuttingIt(t *testing.T) {
	prefix := strings.Repeat("a", 2048) + " "
	for _, secret := range []string{"API_KEY=value", "Authorization: Bearer value", "--password value", `{"password":"value"}`, "sk-test-value", "https://user:password@example.test", "--user user:password", "AKIAIOSFODNN7EXAMPLE", "eyJheader.payload.signature"} {
		if got, want := Text(prefix+secret), prefix+Text(secret); got != want {
			t.Errorf("long text was cut or left unmasked for %q", secret)
		}
	}
	for _, text := range []string{strings.Repeat("界", 400), strings.Repeat("é", 600), strings.Repeat("a", 2048)} {
		if got := Text(text); got != text {
			t.Error("ordinary long text was cut")
		}
	}
}

func TestEnvMasksSeparatedPWD(t *testing.T) {
	r := Env([]string{"MYSQL_PWD=fake-password-123"})
	if r == nil || r.Replace("fake-password-123") != "[redacted]" {
		t.Fatal("MYSQL_PWD value was not masked")
	}
	r = Env([]string{"SMTP_PASS=fake-password-456"})
	if r == nil || r.Replace("fake-password-456") != "[redacted]" {
		t.Fatal("SMTP_PASS value was not masked")
	}
}
