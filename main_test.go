package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func providerResponse(slug, name, status string) string {
	return fmt.Sprintf(`{"data":{"slug":%q,"name":%q,"currentStatus":{"code":%q,"label":%q,"headline":"Live headline"},"source":{"checkedAt":"2026-08-05T00:00:00Z","name":"Official status"},"counts":{"activeIncidents":1},"services":[{"slug":"%s-api","name":"%s API","status":%q}],"activeIncidents":[{"slug":"incident","title":"Incident title","status":"investigating","impact":"minor"}]}}`, slug, name, status, status, slug, name, status)
}

func withProviderServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Setenv("OUTAGEDECK_API_BASE_URL", server.URL)
	t.Setenv("OUTAGEDECK_API_KEY", "")
	return server
}

func TestDefaultChecksGitHubAndServices(t *testing.T) {
	server := withProviderServer(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/providers/github" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		if request.Header.Get("User-Agent") != "gh-outagedeck/dev (+https://github.com/outagedeck/gh-outagedeck)" {
			t.Fatalf("unexpected user agent: %s", request.Header.Get("User-Agent"))
		}
		fmt.Fprint(writer, providerResponse("github", "GitHub", "operational"))
	})
	defer server.Close()

	var stdout, stderr bytes.Buffer
	exit := run(nil, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	for _, expected := range []string{
		"OK GitHub: operational",
		"OK GitHub API",
		"Source checked: 2026-08-05T00:00:00Z",
		"utm_campaign=gh_extension",
		"/alerts?",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("missing %q in output:\n%s", expected, stdout.String())
		}
	}
}

func TestStatusThresholdReturnsTwo(t *testing.T) {
	server := withProviderServer(t, func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, providerResponse("openai", "OpenAI", "partial_outage"))
	})
	defer server.Close()

	var stdout, stderr bytes.Buffer
	exit := run([]string{"--json", "--fail-on=outage", "openai"}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %s", exit, stderr.String())
	}
	for _, expected := range []string{`"status": "partial_outage"`, `"services"`, `"incidents"`} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("missing %q in JSON: %s", expected, stdout.String())
		}
	}
}

func TestMultipleProvidersPreserveOrder(t *testing.T) {
	server := withProviderServer(t, func(writer http.ResponseWriter, request *http.Request) {
		slug := strings.TrimPrefix(request.URL.Path, "/providers/")
		fmt.Fprint(writer, providerResponse(slug, strings.ToUpper(slug), "operational"))
	})
	defer server.Close()

	var stdout, stderr bytes.Buffer
	exit := run([]string{"github,openai", "cloudflare"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	output := stdout.String()
	githubIndex := strings.Index(output, "GITHUB")
	openAIIndex := strings.Index(output, "OPENAI")
	cloudflareIndex := strings.Index(output, "CLOUDFLARE")
	if !(githubIndex >= 0 && githubIndex < openAIIndex && openAIIndex < cloudflareIndex) {
		t.Fatalf("providers not in input order:\n%s", output)
	}
}

func TestServicesCanBeHidden(t *testing.T) {
	server := withProviderServer(t, func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, providerResponse("github", "GitHub", "operational"))
	})
	defer server.Close()

	var stdout, stderr bytes.Buffer
	exit := run([]string{"--services=false", "github"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	if strings.Contains(stdout.String(), "GitHub API") {
		t.Fatalf("service output was not hidden: %s", stdout.String())
	}
}

func TestAPIKeyIsSentOnlyFromEnvironment(t *testing.T) {
	server := withProviderServer(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-API-Key") != "test-key" {
			t.Fatalf("missing API key header")
		}
		fmt.Fprint(writer, providerResponse("github", "GitHub", "operational"))
	})
	defer server.Close()
	t.Setenv("OUTAGEDECK_API_KEY", "test-key")

	var stdout, stderr bytes.Buffer
	if exit := run([]string{"github"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	if strings.Contains(stdout.String(), "test-key") {
		t.Fatal("API key leaked into output")
	}
}

func TestHTTPErrorFailsClosed(t *testing.T) {
	server := withProviderServer(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
		fmt.Fprint(writer, `{"error":{"message":"provider not found"}}`)
	})
	defer server.Close()

	var stdout, stderr bytes.Buffer
	exit := run([]string{"missing"}, &stdout, &stderr)
	if exit != 1 {
		t.Fatalf("exit = %d, want 1", exit)
	}
	if !strings.Contains(stdout.String(), "provider not found") || strings.Contains(stdout.String(), "operational") {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestMalformedJSONFailsClosed(t *testing.T) {
	server := withProviderServer(t, func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, `{not-json`)
	})
	defer server.Close()

	var stdout, stderr bytes.Buffer
	exit := run([]string{"github"}, &stdout, &stderr)
	if exit != 1 || !strings.Contains(stdout.String(), "decode OutageDeck response") {
		t.Fatalf("exit = %d, output = %s", exit, stdout.String())
	}
}

func TestNormalizeProviders(t *testing.T) {
	providers, err := normalizeProviders([]string{"GitHub,openai", "github"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(providers, ",") != "github,openai" {
		t.Fatalf("providers = %v", providers)
	}
	if _, err := normalizeProviders([]string{"bad slug"}); err == nil {
		t.Fatal("expected invalid slug error")
	}
	if _, err := normalizeProviders([]string{"-github"}); err == nil {
		t.Fatal("expected edge-hyphen error")
	}
}

func TestProviderLimit(t *testing.T) {
	values := make([]string, 13)
	for index := range values {
		values[index] = fmt.Sprintf("provider-%d", index)
	}
	if _, err := normalizeProviders(values); err == nil || !strings.Contains(err.Error(), "at most 12") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSearch(t *testing.T) {
	server := withProviderServer(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/status" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		fmt.Fprint(writer, `{"data":{"providers":[{"slug":"anthropic","name":"Anthropic","tagline":"Claude status","currentStatus":{"code":"operational"}},{"slug":"github","name":"GitHub","tagline":"Source control","currentStatus":{"code":"operational"}}]}}`)
	})
	defer server.Close()

	var stdout, stderr bytes.Buffer
	exit := run([]string{"search", "Claude"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	if !strings.Contains(stdout.String(), "anthropic") || strings.Contains(stdout.String(), "github") {
		t.Fatalf("unexpected search output: %s", stdout.String())
	}
}

func TestSearchNoMatchCarriesAttribution(t *testing.T) {
	server := withProviderServer(t, func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, `{"data":{"providers":[]}}`)
	})
	defer server.Close()

	var stdout, stderr bytes.Buffer
	exit := run([]string{"search", "missing"}, &stdout, &stderr)
	if exit != 0 || !strings.Contains(stdout.String(), "utm_campaign=gh_extension") {
		t.Fatalf("exit = %d, output = %s", exit, stdout.String())
	}
}

func TestAlertsBuildsStackSpecificAttributedURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := run([]string{"alerts", "GitHub,cloudflare", "github"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	for _, expected := range []string{
		"Set up alerts for github, cloudflare:",
		"stack=github%2Ccloudflare",
		"utm_campaign=gh_extension",
		"utm_content=alerts_command",
		"selected stack will already be filled in after sign-in",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("missing %q in output:\n%s", expected, stdout.String())
		}
	}
}

func TestAlertsRejectsInvalidProvider(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := run([]string{"alerts", "bad slug"}, &stdout, &stderr)
	if exit != 1 || !strings.Contains(stderr.String(), "invalid provider slug") {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
}

func writeRepositoryFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectRepositoryProviders(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepositoryFile(t, root, ".github/workflows/deploy.yml", "uses: aws-actions/configure-aws-credentials@v5\nuses: cloudflare/wrangler-action@v3\n")
	writeRepositoryFile(t, root, "package.json", `{"dependencies":{"openai":"latest","@sentry/node":"latest"}}`)
	writeRepositoryFile(t, root, "node_modules/example/package.json", `{"dependencies":{"@supabase/supabase-js":"latest"}}`)

	providers, sources, err := detectRepositoryProviders(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(providers, ","); got != "github,aws,cloudflare,openai,sentry" {
		t.Fatalf("providers = %s", got)
	}
	if !strings.Contains(strings.Join(sources["github"], ","), ".github/workflows/deploy.yml") {
		t.Fatalf("github sources = %v", sources["github"])
	}
	if strings.Contains(strings.Join(providers, ","), "supabase") {
		t.Fatalf("ignored dependency was detected: %v", providers)
	}
}

func TestStackChecksDetectedProvidersAndCarriesAttribution(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepositoryFile(t, root, "wrangler.toml", "name = \"edge-worker\"\n")
	server := withProviderServer(t, func(writer http.ResponseWriter, request *http.Request) {
		slug := strings.TrimPrefix(request.URL.Path, "/providers/")
		fmt.Fprint(writer, providerResponse(slug, strings.ToUpper(slug), "operational"))
	})
	defer server.Close()

	var stdout, stderr bytes.Buffer
	exit := run([]string{"stack", "--path", root, "--services=false", "--fail-on=never"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	for _, expected := range []string{
		"Detected repository stack: github, cloudflare",
		"OK GITHUB",
		"OK CLOUDFLARE",
		"stack=github%2Ccloudflare",
		"utm_content=repository_stack",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("missing %q in output:\n%s", expected, stdout.String())
		}
	}
}

func TestStackFailsWhenNothingIsDetected(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := run([]string{"stack", "--path", t.TempDir()}, &stdout, &stderr)
	if exit != 1 || !strings.Contains(stderr.String(), "no recognized") {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
}

func TestVersionAndHelpDoNotUseNetwork(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--help"}} {
		var stdout, stderr bytes.Buffer
		if exit := run(args, &stdout, &stderr); exit != 0 {
			t.Fatalf("args %v exit = %d", args, exit)
		}
		if stdout.Len() == 0 {
			t.Fatalf("args %v produced no output", args)
		}
	}
}
