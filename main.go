package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var version = "dev"

const (
	defaultAPIBase = "https://outagedeck.com/api/v1"
	campaignQuery  = "utm_source=github_cli&utm_medium=extension&utm_campaign=gh_extension"
)

type currentStatus struct {
	Code     string `json:"code"`
	Label    string `json:"label"`
	Headline string `json:"headline"`
	Summary  string `json:"summary"`
}

type source struct {
	CheckedAt   string `json:"checkedAt"`
	Name        string `json:"name"`
	OfficialURL string `json:"officialUrl"`
}

type counts struct {
	ActiveIncidents int `json:"activeIncidents"`
}

type service struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

type incident struct {
	Slug   string `json:"slug"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Impact string `json:"impact"`
}

type provider struct {
	Slug             string        `json:"slug"`
	Name             string        `json:"name"`
	Tagline          string        `json:"tagline"`
	ShortDescription string        `json:"shortDescription"`
	CurrentStatus    currentStatus `json:"currentStatus"`
	Source           source        `json:"source"`
	Counts           counts        `json:"counts"`
	Services         []service     `json:"services"`
	ActiveIncidents  []incident    `json:"activeIncidents"`
}

type providerEnvelope struct {
	Data provider `json:"data"`
}

type statusEnvelope struct {
	Data struct {
		Providers []provider `json:"providers"`
	} `json:"data"`
}

type errorEnvelope struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
	Message string `json:"message"`
}

type result struct {
	Slug            string     `json:"slug"`
	Name            string     `json:"name"`
	Status          string     `json:"status"`
	Label           string     `json:"label"`
	Headline        string     `json:"headline,omitempty"`
	ActiveIncidents int        `json:"activeIncidents"`
	CheckedAt       string     `json:"checkedAt,omitempty"`
	SourceName      string     `json:"sourceName,omitempty"`
	SourceURL       string     `json:"sourceUrl,omitempty"`
	Services        []service  `json:"services,omitempty"`
	Incidents       []incident `json:"incidents,omitempty"`
	URL             string     `json:"url"`
	Error           string     `json:"error,omitempty"`
}

var statusRanks = map[string]int{
	"operational":    0,
	"maintenance":    1,
	"unknown":        1,
	"degraded":       2,
	"partial_outage": 3,
	"major_outage":   4,
}

var failureRanks = map[string]int{
	"degraded":     2,
	"outage":       3,
	"major_outage": 4,
	"never":        1 << 30,
}

type providerRule struct {
	Slug    string
	Paths   []string
	Markers []string
}

var repositoryProviderRules = []providerRule{
	{Slug: "aws", Markers: []string{"aws-actions/", "@aws-sdk/", "amazonaws.com", "boto3", "provider \"aws\""}},
	{Slug: "cloudflare", Paths: []string{"wrangler.toml", "wrangler.json", "wrangler.jsonc"}, Markers: []string{"cloudflare/wrangler-action", "@cloudflare/", "cloudflare-go", "wrangler deploy"}},
	{Slug: "google-cloud", Markers: []string{"google-github-actions/", "@google-cloud/", "google.cloud/", "google-cloud", "provider \"google\""}},
	{Slug: "azure", Markers: []string{"azure/", "@azure/", "azure-identity", "provider \"azurerm\"", "mcr.microsoft.com/azure"}},
	{Slug: "openai", Markers: []string{"\"openai\"", "openai/", "github.com/openai/", "from openai import", "import openai"}},
	{Slug: "anthropic", Markers: []string{"\"anthropic\"", "@anthropic-ai/", "github.com/anthropics/", "from anthropic import", "import anthropic"}},
	{Slug: "vercel", Paths: []string{"vercel.json"}, Markers: []string{"vercel/action", "vercel deploy", "\"vercel\""}},
	{Slug: "netlify", Paths: []string{"netlify.toml"}, Markers: []string{"netlify/actions", "netlify deploy", "\"netlify-cli\""}},
	{Slug: "sentry", Markers: []string{"getsentry/", "@sentry/", "sentry-sdk", "sentry_sdk"}},
	{Slug: "datadog", Markers: []string{"datadog/", "dd-trace", "datadog-api-client"}},
	{Slug: "newrelic", Markers: []string{"newrelic/", "newrelic", "new-relic"}},
	{Slug: "grafana", Markers: []string{"grafana/", "grafana-agent", "grafana-cloud"}},
	{Slug: "slack", Markers: []string{"slackapi/", "@slack/", "slack-sdk", "slack_sdk"}},
	{Slug: "firebase", Paths: []string{"firebase.json"}, Markers: []string{"firebase-tools", "firebase-admin", "firebase/app", "google.golang.org/api/firebase"}},
	{Slug: "supabase", Markers: []string{"supabase/", "@supabase/", "supabase-py"}},
	{Slug: "circleci", Paths: []string{".circleci/config.yml", ".circleci/config.yaml"}, Markers: []string{"circleci/"}},
	{Slug: "gitlab", Paths: []string{".gitlab-ci.yml", ".gitlab-ci.yaml"}, Markers: []string{"gitlab.com/"}},
	{Slug: "bitbucket", Paths: []string{"bitbucket-pipelines.yml", "bitbucket-pipelines.yaml"}, Markers: []string{"bitbucket.org/"}},
	{Slug: "docker", Paths: []string{"dockerfile", ".dockerignore", "docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}, Markers: []string{"docker/build-push-action", "docker/login-action", "docker.io/"}},
	{Slug: "heroku", Paths: []string{"procfile", "heroku.yml", "app.json"}, Markers: []string{"heroku/", "heroku container:"}},
	{Slug: "render", Paths: []string{"render.yaml", "render.yml"}, Markers: []string{"render.com/"}},
	{Slug: "digitalocean", Markers: []string{"digitalocean/action-", "doctl", "digitalocean/godo"}},
	{Slug: "twilio", Markers: []string{"\"twilio\"", "twilio/twilio-", "github.com/twilio/", "from twilio"}},
}

var repositoryScanNames = map[string]bool{
	".gitlab-ci.yml": true, ".gitlab-ci.yaml": true, ".tool-versions": true,
	"app.json": true, "bitbucket-pipelines.yml": true, "bitbucket-pipelines.yaml": true,
	"build.gradle": true, "build.gradle.kts": true, "cargo.lock": true, "cargo.toml": true,
	"compose.yaml": true, "compose.yml": true, "docker-compose.yaml": true, "docker-compose.yml": true,
	"dockerfile": true, "firebase.json": true, "flake.nix": true, "gemfile": true, "gemfile.lock": true,
	"go.mod": true, "go.sum": true, "gradle.properties": true, "heroku.yml": true, "netlify.toml": true,
	"package-lock.json": true, "package.json": true, "pipfile": true, "pnpm-lock.yaml": true,
	"poetry.lock": true, "pom.xml": true, "procfile": true, "pyproject.toml": true, "render.yaml": true,
	"render.yml": true, "requirements.txt": true, "vercel.json": true, "wrangler.json": true,
	"wrangler.jsonc": true, "wrangler.toml": true, "yarn.lock": true,
}

var repositorySkipDirectories = map[string]bool{
	".git": true, ".next": true, ".venv": true, "build": true, "coverage": true,
	"dist": true, "node_modules": true, "target": true, "vendor": true, "venv": true,
}

func apiBase() string {
	if value := strings.TrimRight(os.Getenv("OUTAGEDECK_API_BASE_URL"), "/"); value != "" {
		return value
	}
	return defaultAPIBase
}

func pageURL(path string) string {
	return "https://outagedeck.com" + path + "?" + campaignQuery
}

func providerURL(slug string) string {
	return pageURL("/providers/" + url.PathEscape(slug))
}

func attributedStackAlertsURL(slugs []string, content string) string {
	query := url.Values{}
	query.Set("stack", strings.Join(slugs, ","))
	query.Set("utm_source", "github_cli")
	query.Set("utm_medium", "extension")
	query.Set("utm_campaign", "gh_extension")
	query.Set("utm_content", content)
	return "https://outagedeck.com/account?" + query.Encode()
}

func stackAlertsURL(slugs []string) string {
	return attributedStackAlertsURL(slugs, "alerts_command")
}

func requestJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gh-outagedeck/"+version+" (+https://github.com/outagedeck/gh-outagedeck)")
	if apiKey := strings.TrimSpace(os.Getenv("OUTAGEDECK_API_KEY")); apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload errorEnvelope
		_ = json.NewDecoder(response.Body).Decode(&payload)
		message := payload.Error.Message
		if message == "" {
			message = payload.Message
		}
		if message == "" {
			message = response.Status
		}
		return errors.New(message)
	}

	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode OutageDeck response: %w", err)
	}
	return nil
}

func fetchProvider(ctx context.Context, client *http.Client, slug string) result {
	var payload providerEnvelope
	endpoint := apiBase() + "/providers/" + url.PathEscape(slug)
	if err := requestJSON(ctx, client, endpoint, &payload); err != nil {
		return result{Slug: slug, Name: slug, Status: "error", URL: providerURL(slug), Error: err.Error()}
	}

	data := payload.Data
	if data.Name == "" || data.CurrentStatus.Code == "" {
		return result{Slug: slug, Name: slug, Status: "error", URL: providerURL(slug), Error: "unexpected provider response"}
	}
	label := data.CurrentStatus.Label
	if label == "" {
		label = data.CurrentStatus.Code
	}
	headline := data.CurrentStatus.Headline
	if headline == "" {
		headline = data.CurrentStatus.Summary
	}
	return result{
		Slug:            data.Slug,
		Name:            data.Name,
		Status:          data.CurrentStatus.Code,
		Label:           label,
		Headline:        headline,
		ActiveIncidents: data.Counts.ActiveIncidents,
		CheckedAt:       data.Source.CheckedAt,
		SourceName:      data.Source.Name,
		SourceURL:       data.Source.OfficialURL,
		Services:        data.Services,
		Incidents:       data.ActiveIncidents,
		URL:             providerURL(data.Slug),
	}
}

func normalizeProviders(values []string) ([]string, error) {
	if len(values) == 0 {
		return []string{"github"}, nil
	}
	seen := make(map[string]bool)
	providers := make([]string, 0, len(values))
	for _, value := range values {
		for _, raw := range strings.Split(value, ",") {
			slug := strings.ToLower(strings.TrimSpace(raw))
			if slug == "" || seen[slug] {
				continue
			}
			for index, character := range slug {
				valid := character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-'
				if !valid || character == '-' && (index == 0 || index == len(slug)-1) {
					return nil, fmt.Errorf("invalid provider slug: %s", slug)
				}
			}
			seen[slug] = true
			providers = append(providers, slug)
		}
	}
	if len(providers) == 0 {
		return nil, errors.New("provide at least one provider slug")
	}
	if len(providers) > 12 {
		return nil, errors.New("at most 12 providers can be checked at once")
	}
	return providers, nil
}

func repositoryPathMatches(path string, values []string) bool {
	for _, value := range values {
		if path == value || strings.HasSuffix(path, "/"+value) {
			return true
		}
	}
	return false
}

func shouldScanRepositoryFile(path string) bool {
	lowerPath := strings.ToLower(filepath.ToSlash(path))
	base := strings.ToLower(filepath.Base(lowerPath))
	if repositoryScanNames[base] || strings.Contains(lowerPath, "/.github/workflows/") || strings.Contains(lowerPath, "/.circleci/") {
		return true
	}
	return strings.HasSuffix(base, ".tf") || strings.HasSuffix(base, ".hcl")
}

func detectRepositoryProviders(root string) ([]string, map[string][]string, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	rootInfo, err := os.Stat(absoluteRoot)
	if err != nil {
		return nil, nil, err
	}
	if !rootInfo.IsDir() {
		return nil, nil, fmt.Errorf("repository path is not a directory: %s", root)
	}

	hits := make(map[string]map[string]bool)
	addHit := func(slug, source string) {
		if hits[slug] == nil {
			hits[slug] = make(map[string]bool)
		}
		hits[slug][source] = true
	}
	if _, err := os.Stat(filepath.Join(absoluteRoot, ".git")); err == nil {
		addHit("github", ".git")
	}

	filesScanned := 0
	err = filepath.WalkDir(absoluteRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != absoluteRoot && repositorySkipDirectories[strings.ToLower(entry.Name())] {
				return filepath.SkipDir
			}
			return nil
		}
		if filesScanned >= 2000 {
			return nil
		}

		relativePath, err := filepath.Rel(absoluteRoot, path)
		if err != nil {
			return err
		}
		source := filepath.ToSlash(relativePath)
		lowerPath := strings.ToLower(source)
		if strings.HasPrefix(lowerPath, ".github/workflows/") {
			addHit("github", source)
		}
		for _, rule := range repositoryProviderRules {
			if repositoryPathMatches(lowerPath, rule.Paths) {
				addHit(rule.Slug, source)
			}
		}
		if !shouldScanRepositoryFile("/" + source) {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 1024*1024 {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		filesScanned++
		if strings.IndexByte(string(contents), 0) >= 0 {
			return nil
		}
		lowerContents := strings.ToLower(string(contents))
		for _, rule := range repositoryProviderRules {
			for _, marker := range rule.Markers {
				if strings.Contains(lowerContents, marker) {
					addHit(rule.Slug, source)
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	providers := make([]string, 0, len(hits))
	if len(hits["github"]) > 0 {
		providers = append(providers, "github")
	}
	for _, rule := range repositoryProviderRules {
		if len(hits[rule.Slug]) > 0 {
			providers = append(providers, rule.Slug)
		}
	}

	sources := make(map[string][]string, len(providers))
	for _, slug := range providers {
		for source := range hits[slug] {
			sources[slug] = append(sources[slug], source)
		}
		sort.Strings(sources[slug])
	}
	return providers, sources, nil
}

func marker(status string) string {
	if status == "operational" {
		return "OK"
	}
	return "!!"
}

func statusCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("outagedeck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	failOn := flags.String("fail-on", "degraded", "degraded, outage, major_outage, or never")
	showServices := flags.Bool("services", true, "show provider services")
	timeout := flags.Duration("timeout", 10*time.Second, "HTTP timeout")
	if err := flags.Parse(args); err != nil {
		return 1
	}

	threshold, ok := failureRanks[strings.ToLower(*failOn)]
	if !ok {
		fmt.Fprintln(stderr, "--fail-on must be degraded, outage, major_outage, or never")
		return 1
	}
	if *timeout <= 0 {
		fmt.Fprintln(stderr, "--timeout must be positive")
		return 1
	}
	providers, err := normalizeProviders(flags.Args())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	ctx := context.Background()
	client := &http.Client{Timeout: *timeout}
	results := make([]result, len(providers))
	done := make(chan int, len(providers))
	for index, slug := range providers {
		go func(index int, slug string) {
			results[index] = fetchProvider(ctx, client, slug)
			done <- index
		}(index, slug)
	}
	for range providers {
		<-done
	}

	hasError := false
	hasFailure := false
	for _, item := range results {
		if item.Error != "" {
			hasError = true
			continue
		}
		if rank, exists := statusRanks[item.Status]; !exists || rank >= threshold {
			hasFailure = true
		}
	}

	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(results); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else {
		for resultIndex, item := range results {
			if resultIndex > 0 {
				fmt.Fprintln(stdout)
			}
			if item.Error != "" {
				fmt.Fprintf(stdout, "! %s: check failed: %s\n", item.Name, item.Error)
				continue
			}
			fmt.Fprintf(stdout, "%s %s: %s", marker(item.Status), item.Name, item.Label)
			if item.Headline != "" {
				fmt.Fprintf(stdout, ": %s", item.Headline)
			}
			fmt.Fprintln(stdout)
			if *showServices {
				for _, service := range item.Services {
					fmt.Fprintf(stdout, "   %s %-22s %s\n", marker(service.Status), service.Name, service.Status)
				}
			}
			if item.CheckedAt != "" {
				fmt.Fprintf(stdout, "   Source checked: %s", item.CheckedAt)
				if item.SourceName != "" {
					fmt.Fprintf(stdout, " (%s)", item.SourceName)
				}
				fmt.Fprintln(stdout)
			}
			fmt.Fprintf(stdout, "   Details: %s\n", item.URL)
			if item.ActiveIncidents > 0 {
				fmt.Fprintf(stdout, "   Alerts:  %s\n", pageURL("/alerts"))
			}
		}
	}

	if hasError {
		return 1
	}
	if hasFailure {
		return 2
	}
	return 0
}

func searchCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("search", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	limit := flags.Int("limit", 10, "maximum matches")
	timeout := flags.Duration("timeout", 10*time.Second, "HTTP timeout")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	query := strings.ToLower(strings.TrimSpace(strings.Join(flags.Args(), " ")))
	if query == "" {
		fmt.Fprintln(stderr, "provide a provider name or product to search for")
		return 1
	}
	if *limit < 1 || *limit > 50 {
		fmt.Fprintln(stderr, "--limit must be between 1 and 50")
		return 1
	}

	var payload statusEnvelope
	client := &http.Client{Timeout: *timeout}
	if err := requestJSON(context.Background(), client, apiBase()+"/status", &payload); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	matches := make([]provider, 0)
	for _, item := range payload.Data.Providers {
		haystack := strings.ToLower(strings.Join([]string{item.Slug, item.Name, item.Tagline, item.ShortDescription}, " "))
		if strings.Contains(haystack, query) {
			matches = append(matches, item)
		}
	}
	sort.SliceStable(matches, func(left, right int) bool {
		leftExact := strings.EqualFold(matches[left].Slug, query) || strings.EqualFold(matches[left].Name, query)
		rightExact := strings.EqualFold(matches[right].Slug, query) || strings.EqualFold(matches[right].Name, query)
		if leftExact != rightExact {
			return leftExact
		}
		return matches[left].Name < matches[right].Name
	})
	if len(matches) > *limit {
		matches = matches[:*limit]
	}
	if *jsonOutput {
		if err := json.NewEncoder(stdout).Encode(matches); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if len(matches) == 0 {
		fmt.Fprintf(stdout, "No tracked providers matched %q. Browse %s\n", query, pageURL("/providers"))
		return 0
	}
	for _, item := range matches {
		fmt.Fprintf(stdout, "%-24s %-18s %s\n", item.Slug, item.CurrentStatus.Code, item.Name)
	}
	return 0
}

func alertsCommand(args []string, stdout, stderr io.Writer) int {
	providers, err := normalizeProviders(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	fmt.Fprintf(stdout, "Set up alerts for %s:\n", strings.Join(providers, ", "))
	fmt.Fprintln(stdout, stackAlertsURL(providers))
	fmt.Fprintln(stdout, "\nThe selected stack will already be filled in after sign-in.")
	return 0
}

func stackCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("stack", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("path", ".", "repository directory to inspect")
	failOn := flags.String("fail-on", "degraded", "degraded, outage, major_outage, or never")
	showServices := flags.Bool("services", true, "show provider services")
	timeout := flags.Duration("timeout", 10*time.Second, "HTTP timeout")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "stack accepts flags only; use --path to choose a repository")
		return 1
	}

	providers, sources, err := detectRepositoryProviders(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(providers) == 0 {
		fmt.Fprintln(stderr, "no recognized cloud or SaaS dependencies found")
		return 1
	}
	if len(providers) > 12 {
		fmt.Fprintf(stderr, "detected %d providers; checking the first 12\n", len(providers))
		providers = providers[:12]
	}

	fmt.Fprintf(stdout, "Detected repository stack: %s\n", strings.Join(providers, ", "))
	for _, slug := range providers {
		foundIn := sources[slug]
		if len(foundIn) > 3 {
			fmt.Fprintf(stdout, "  %-16s %s (+%d more)\n", slug, strings.Join(foundIn[:3], ", "), len(foundIn)-3)
			continue
		}
		fmt.Fprintf(stdout, "  %-16s %s\n", slug, strings.Join(foundIn, ", "))
	}
	fmt.Fprintln(stdout)

	statusArgs := []string{
		"--fail-on=" + *failOn,
		fmt.Sprintf("--services=%t", *showServices),
		"--timeout=" + timeout.String(),
		strings.Join(providers, ","),
	}
	exit := statusCommand(statusArgs, stdout, stderr)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Keep this detected stack on watch:")
	fmt.Fprintln(stdout, attributedStackAlertsURL(providers, "repository_stack"))
	return exit
}

func usage(writer io.Writer) {
	fmt.Fprintln(writer, `OutageDeck for GitHub CLI: verify dependency status before debugging

Usage:
  gh outagedeck [flags] [provider...]
  gh outagedeck search [flags] <query>
  gh outagedeck stack [flags]
  gh outagedeck alerts [provider...]
  gh outagedeck version

With no provider, the command checks GitHub, GitHub Actions, GitHub API,
and GitHub Web. Add provider slugs to check a dependency stack.

Examples:
  gh outagedeck
  gh outagedeck github cloudflare openai
  gh outagedeck --json --fail-on=outage github anthropic
  gh outagedeck stack
  gh outagedeck alerts github cloudflare openai
  gh outagedeck search "Claude"

Environment:
  OUTAGEDECK_API_KEY       optional higher-quota API key
  OUTAGEDECK_API_BASE_URL  API override for testing`)
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "search":
			return searchCommand(args[1:], stdout, stderr)
		case "alerts":
			return alertsCommand(args[1:], stdout, stderr)
		case "stack":
			return stackCommand(args[1:], stdout, stderr)
		case "version", "--version", "-v":
			fmt.Fprintln(stdout, version)
			return 0
		case "help", "--help", "-h":
			usage(stdout)
			return 0
		case "status", "check":
			return statusCommand(args[1:], stdout, stderr)
		}
	}
	return statusCommand(args, stdout, stderr)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
