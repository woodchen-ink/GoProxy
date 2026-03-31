# Project Notes

## Proxy Behavior

- SOCKS5 inbound requests support three DNS modes: `remote`, `fallback`, and `local`.
- `fallback` first lets the upstream SOCKS5 resolve the hostname, then retries with locally resolved IP addresses if that fails.
- `local` always resolves the hostname on the GoProxy host before forwarding the request upstream.
- `fallback` and `local` improve compatibility with flaky free SOCKS5 upstreams, but they may leak a local DNS query.
- SOCKS5 inbound traffic defaults to SOCKS5 upstreams only, but can optionally reuse HTTP upstreams that support CONNECT.
- The fast SOCKS5 source set also includes `https://socks5-proxy.github.io/`, which is parsed from embedded `socks5://ip:port` links in the HTML page.

## Config

- `SOCKS5_DNS_MODE` defaults to `fallback`.
- Set `SOCKS5_DNS_MODE=local` for pure local DNS.
- `SOCKS5_ALLOW_HTTP_UPSTREAM` defaults to `false`.
- Legacy `SOCKS5_LOCAL_DNS_FALLBACK=true/false` is still accepted for backward compatibility, but the new mode variable takes precedence.
