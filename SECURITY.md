# Security

Omarchy Remote can type on your keyboard and shut down your PC, so security problems matter more here than in most small projects.

## Reporting a problem

Please don't open a public issue. Report it privately instead, from the repository's **Security** tab: **Report a vulnerability**. Only you and the maintainer can see the report.

Include what you found, how to reproduce it, and which version (`pc-remote version`). You'll get a reply within a week, and a fix is released as a new version.

## Supported versions

Only the latest release gets fixes. `install.sh` and `pc-remote setup` always install the latest one.

## What counts

For example: getting in without the passcode, getting past the login rate limit, a guest controlling more than their controller, reaching the app from outside the home network or tailnet when the settings say it shouldn't be, or setup opening the firewall wider than it shows.

How to run it safely is covered in the README's [Security](README.md#security) section.
