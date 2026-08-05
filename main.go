package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
				fmt.Fprintf(stdout, " — %s", item.Headline)
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

func usage(writer io.Writer) {
	fmt.Fprintln(writer, `OutageDeck for GitHub CLI — verify dependency status before debugging

Usage:
  gh outagedeck [flags] [provider...]
  gh outagedeck search [flags] <query>
  gh outagedeck version

With no provider, the command checks GitHub, GitHub Actions, GitHub API,
and GitHub Web. Add provider slugs to check a dependency stack.

Examples:
  gh outagedeck
  gh outagedeck github cloudflare openai
  gh outagedeck --json --fail-on=outage github anthropic
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
