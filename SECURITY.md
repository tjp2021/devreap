# Security Policy

## Scope

devreap sends signals to processes on your machine. A vulnerability in devreap could allow unintended process termination or privilege escalation. We take this seriously.

## Reporting a Vulnerability

**Do not open a public issue for security vulnerabilities.**

Email: **engineering@iteachyouai.com**

Include:
- Description of the vulnerability
- Steps to reproduce
- Impact assessment
- Suggested fix (if any)

## Response Timeline

- **Acknowledgement**: within 48 hours
- **Initial assessment**: within 1 week
- **Fix or mitigation**: within 30 days for critical issues

## Supported Versions

Only the latest release is supported with security updates.

| Version | Supported |
|---------|-----------|
| Latest  | Yes       |
| < Latest | No       |

## Security Design

devreap includes multiple safety layers:

- **PID reuse protection** — verifies process name before sending signals
- **User isolation** — only kills processes owned by the current user
- **Blocklist** — system processes (postgres, redis, nginx, sshd, etc.) are protected
- **PID 1 / self / parent protection** — hardcoded, cannot be overridden by config
- **Config validation** — rejects dangerous threshold/weight values at startup
- **No network access** — devreap never opens ports or makes network requests
- **No privilege escalation** — runs as current user, never requires root
