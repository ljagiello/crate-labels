# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub:
**[Report a vulnerability](https://github.com/ljagiello/crate-labels/security/advisories/new)**
(the repository's Security tab → "Report a vulnerability"). Don't open a public issue.

Include the version (`crate-labels version`), your macOS version, and steps or a
proof of concept. This is a spare-time project: you'll get a reply as soon as the maintainer
can manage, and a fix and advisory are coordinated with you before anything is public.

## Supported versions

Only the latest release gets security fixes.

## Security model

crate-labels runs a web server on your Mac for its UI. The full model is in
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md#security-model-of-the-local-server). In short:

- **Localhost only:** the server listens on 127.0.0.1, and the `Host` header must be
  `127.0.0.1`, `localhost` or `::1`. That blocks DNS rebinding.
- **Writes from the UI only:** a POST, PUT, PATCH or DELETE must come from the server's own
  origin, and it must be `application/json`. Requests with `Origin: null`, another port or
  `Sec-Fetch-Site: cross-site` are refused, so other websites can't print or change data.
  The only exception is the audio upload to `POST /api/tempo`, which takes
  `application/octet-stream`. That type also needs a CORS preflight, which the server never
  grants. Uploads are capped at 400 MB.
- **No framing:** every response sends `X-Frame-Options: DENY`,
  `Content-Security-Policy: frame-ancestors 'none'` and `X-Content-Type-Options: nosniff`.
- **Discogs token:** it is stored in the macOS Keychain, and sent only to
  `https://api.discogs.com`. It is never logged or written to the database.
- **External programs:** `lp`, `lpstat`, `lpoptions`, `security`, `afconvert`, `ffmpeg`
  and `yt-dlp` run with argument lists, never through a shell. Secrets go on stdin, and file
  paths and video ids are validated so they can't be read as options.

Good reports include a web page that can make the local server act, a way to read or leak
the Discogs token, and input (a Discogs response, an audio file, a printer reply) that
makes crate-labels run commands or write files it shouldn't.

Out of scope:

- anything that needs code already running as your macOS user;
- Bluetooth label printers accepting jobs from anyone nearby, which is how the printers
  themselves work;
- Gatekeeper warnings about the unsigned release binary.
