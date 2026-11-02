# Security policy

Arena sells tickets and takes payments for real events, so please report a
security problem **privately**, not in a public issue or pull request.

## How to report

Use GitHub's private vulnerability reporting: the **Security** tab of this
repository, then **Report a vulnerability**. Please include:

- what an attacker can do and to whom (another organization, a buyer, the platform);
- the steps or the request that shows it;
- the version you tested (`GET /v1/info` answers the commit) and whether it was
  your own deployment.

Do not test against `arenasoldout.com` or another live deployment with real
organizers and buyers: run the stack locally (`docker compose up`, see README).

## What to expect

We answer within a few working days, keep you informed while the fix is made,
and publish the fix and an advisory once it is deployed. We do not describe a
problem publicly before the fix reaches production.

## Scope

In scope: this repository's code (API, worker, Telegram bot, web event center,
admin console, widget, tickets and shop pages, WordPress plugin).

Out of scope: the hosting and third-party services Arena uses (payment
providers, e-mail, Telegram, Cloudflare), and denial-of-service by volume.
