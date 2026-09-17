# Security policy

## Reporting a vulnerability

Please do not open a public issue for a security problem. Report it
privately through GitHub's vulnerability reporting form:

https://github.com/ChristopherDavenport/openresponses/security/advisories/new

Include the affected version, a description of the issue and, if you
have one, a minimal reproduction. You will get an acknowledgement within
a week. Once a fix is available it ships as a new patch version and the
advisory is published with credit to the reporter, unless you prefer to
stay anonymous.

## Supported versions

Only the latest release receives security fixes.

## Scope

This library parses untrusted input on both sides of the wire: request
bodies and WebSocket frames on the server, and response bodies, SSE
frames and WebSocket frames on the client. Anything that lets a peer
crash a process, exhaust its memory, bypass request validation, or plant
data (for example headers) that the other side then forwards is in
scope. The `examples/` programs are illustrations rather than hardened
services.
