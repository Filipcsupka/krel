# Security Policy

## Supported Versions

`krel` is pre-release software. Security fixes are applied to the default branch until versioned releases are published.

## Reporting A Vulnerability

Please report security issues privately through GitHub Security Advisories when available, or contact the maintainer directly.

Do not open a public issue for vulnerabilities involving credential exposure, cluster access, or sensitive Kubernetes object data.

## Data Handling

`krel` reads data through your local Kubernetes credentials and renders it in your terminal. It does not intentionally mutate cluster resources.

Be careful when sharing terminal output, screenshots, YAML, logs, or issue attachments. Kubernetes objects can contain secrets, internal hostnames, private image references, and application-specific identifiers.
