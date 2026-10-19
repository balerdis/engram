# make sync-catalog CATALOG=/path/to/credential-transport-catalog.json
.PHONY: sync-catalog
sync-catalog:
	@test -n "$(CATALOG)" || { echo "usage: make sync-catalog CATALOG=<path>" >&2; exit 2; }
	@internal/redact/sync-catalog.sh "$(CATALOG)"
