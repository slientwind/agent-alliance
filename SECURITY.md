# Security Policy

## Security Architecture Highlights

Agent Alliance is designed for single-enterprise self-hosting with defense-in-depth boundaries:

- **Personal Space Isolation**: Private personal threads and notes are isolated from other users and excluded from standard admin inspection APIs.
- **Air-Gapped Execution Sandbox**: Code and Office generation runs in a `sandbox-internal` (`internal: true`) Docker network with read-only inputs/templates, dropped capabilities, and zero injected credentials.
- **Row-Level Business Data Security**: OAuth2 OBO tokens and enforced employee/department predicates prevent cross-employee data leakage, backed by SQL AST read-only guards and L2 human-approved idempotent writeback.
- **Decoupled External Agents & Reversible DLP**: External agents never receive platform LLM Gateway keys; outbound commercial agent calls pass through reversible PII/floor-price masking and secret-field blocking.

## Supported Versions

| Version | Supported |
| :--- | :---: |
| `0.2.x` | ✅ Active |
| `< 0.2.0` | ⚠️ Upgrade to `v0.2.1+` |

## Reporting a Vulnerability

If you discover a security vulnerability (such as ACL bypass, SSRF, sandbox escape, DLP leak, or SQL guard bypass), **please do not open a public GitHub issue**.

Instead, use GitHub's **Private Vulnerability Reporting** under the repository's **Security → Advisories** tab, including reproduction steps and affected versions. We will acknowledge reports promptly and coordinate a fix before public disclosure.
