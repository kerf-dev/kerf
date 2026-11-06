# Security policy

## Reporting a vulnerability

Please **do not open a public issue** for a security problem. Report it privately through GitHub:
**Security → Report a vulnerability** on this repository
([private vulnerability reporting](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability)).

If you cannot use GitHub, email **admin@getkerf.dev** instead. GitHub private reporting is preferred.

Include what you found, how to reproduce it, the version (image tag or chart version) and the impact you
expect. We aim to acknowledge a report within 5 working days and to agree on a disclosure date with you.

## Supported versions

Until 1.0.0, only the latest minor release gets security fixes.

## Scope notes

Kerf changes pod resources in your clusters. Reports about these areas are especially welcome:

- the agent's and webhook's RBAC and what a compromised component could change;
- the hub API's authentication (agent tokens, user sessions, API tokens) and authorization (roles);
- the Helm charts' defaults (NetworkPolicies, pod security, secrets handling).

## Verifying releases

Every image and chart is signed with cosign by the release workflow; the release notes show the
`cosign verify` command. Images carry an SPDX SBOM and SLSA provenance.
