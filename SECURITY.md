# Security policy

Please report security issues privately to `security@outagedeck.com` rather than opening a public issue.

This extension is read-only. It sends GET requests only to `https://outagedeck.com/api/v1` unless `OUTAGEDECK_API_BASE_URL` is explicitly set for testing. An optional `OUTAGEDECK_API_KEY` is read from the environment, sent only in the `X-API-Key` request header, and never printed.

For the hosted service's permission model, credential handling, data retention, and current compliance posture, see https://outagedeck.com/security.
