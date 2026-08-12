# OutageDeck for GitHub CLI

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Check whether GitHub or another cloud/SaaS dependency is reporting an incident before debugging your repository, workflow, or deployment.

OutageDeck normalizes official vendor status feeds. It is corroborating evidence, not a replacement for synthetic monitoring.

## Install

```bash
gh extension install outagedeck/gh-outagedeck
```

## Use

With no arguments, `gh outagedeck` checks GitHub and its tracked GitHub Actions, API, and Web services:

```console
$ gh outagedeck
OK GitHub: Operational: All Systems Operational
   OK GitHub Actions         operational
   OK GitHub API             operational
   OK GitHub Web             operational
   Source checked: 2026-08-05T20:30:01.319+00:00 (GitHub Status)
   Details: https://outagedeck.com/providers/github?utm_source=github_cli&utm_medium=extension&utm_campaign=gh_extension
```

Check multiple dependencies:

```bash
gh outagedeck github cloudflare openai anthropic
```

During GitHub's Actions incident on August 6, 2026, the extension kept the still-healthy API separate from the affected services:

```console
$ gh outagedeck github
!! GitHub: Degraded: Minor Service Outage
   !! GitHub Actions         partial_outage
   OK GitHub API             operational
   !! GitHub Web             degraded
   Source checked: 2026-08-06T15:50:39.71+00:00 (GitHub Status)
   Details: https://outagedeck.com/providers/github?utm_source=github_cli&utm_medium=extension&utm_campaign=gh_extension
   Alerts:  https://outagedeck.com/alerts?utm_source=github_cli&utm_medium=extension&utm_campaign=gh_extension
```

This command exited `2` under the default `degraded` threshold. Service rows prevent an Actions incident from being misreported as an API outage, while the non-zero exit remains useful in scripts and pre-debug checks.

Use structured output and a CI-friendly failure threshold:

```bash
gh outagedeck --json --fail-on=outage github openai
```

Find provider slugs:

```bash
gh outagedeck search "Claude"
```

Turn a checked stack into an alert setup link:

```console
$ gh outagedeck alerts github cloudflare openai
Set up alerts for github, cloudflare, openai:
https://outagedeck.com/account?stack=github%2Ccloudflare%2Copenai&utm_campaign=gh_extension&utm_content=alerts_command&utm_medium=extension&utm_source=github_cli

The selected stack will already be filled in after sign-in.
```

The link opens the normal account flow. Free email alerts cover up to five providers, and the selected stack survives the email sign-in round trip.

The status command exits with:

- `0` when every provider is below the selected threshold;
- `1` when an argument, network request, or response fails;
- `2` when at least one provider reaches the selected threshold.

`--fail-on` accepts `degraded` (default), `outage`, `major_outage`, or `never`. Use `--services=false` for provider-only output. Set `OUTAGEDECK_API_KEY` for an optional higher-quota API key; keys are read only from the environment so they do not enter shell history.

## Data and trust

- Public read-only checks need no account or API key.
- The anonymous API quota is 120 requests per hour.
- An unavailable or malformed response is an error, never `operational`.
- Requests identify this open-source client through a standard `User-Agent`; the extension adds no separate telemetry.
- Security, credential handling, data retention, and current compliance posture are documented on the [OutageDeck security page](https://outagedeck.com/security?utm_source=github_cli&utm_medium=extension&utm_campaign=gh_extension).

## Development

```bash
go test ./...
go vet ./...
go build -o gh-outagedeck .
gh extension install .
gh outagedeck
```

## License

MIT
