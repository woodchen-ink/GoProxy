# Project Notes

## Proxy Behavior

- SOCKS5 inbound requests now prefer upstream domain resolution first.
- When `SOCKS5_LOCAL_DNS_FALLBACK=true`, GoProxy retries failed upstream domain connects with locally resolved IP addresses.
- This improves compatibility with flaky free SOCKS5 upstreams, but failed domain-resolution paths may leak one local DNS query.

## Config

- `SOCKS5_LOCAL_DNS_FALLBACK` defaults to `true`.
- Set it to `false` if you need strict remote-DNS behavior and accept more hostname-resolution failures from upstream proxies.
