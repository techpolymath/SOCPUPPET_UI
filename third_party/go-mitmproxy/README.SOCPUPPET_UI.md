# Local go-mitmproxy patch

This directory contains the non-test packages from `github.com/lqqyt2423/go-mitmproxy` v1.9.2 that SOCPUPPET_UI imports. The root module uses a local `replace` directive so compiled binaries do not retain non-English strings from the upstream module.

SOCPUPPET_UI changes:

- translated the direct-proxy request response and certificate errors to English;
- removed non-English comments from the copied source;
- retained the upstream module path and license.

When updating the upstream dependency, repeat the English-only source scan and verify the compiled binary before changing the version recorded here.
